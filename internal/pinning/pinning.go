// Package pinning adds certificate pinning on top of normal chain validation.
//
// Pins are SHA-256 hashes of Subject Public Key Info, and a connection is
// accepted when any certificate in the presented chain matches a stored pin.
// Pinning the whole chain rather than the leaf is what makes this survivable:
// the control endpoint's leaf is a 90-day certificate, so a leaf pin would
// break on every renewal, whereas the issuer's key is stable across them.
//
// This never replaces normal verification. The verifier runs after the standard
// chain check, so a pin can only make acceptance stricter, never looser.
package pinning

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
)

type Mode string

const (
	// ModeOff performs no pinning: system roots alone decide.
	ModeOff Mode = "off"
	// ModeTOFU records the chain on first use and requires a match afterwards.
	ModeTOFU Mode = "tofu"
	// ModeStrict requires a match against pins that are already stored.
	ModeStrict Mode = "strict"
)

func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case ModeOff, ModeTOFU, ModeStrict:
		return Mode(s), nil
	}
	return "", fmt.Errorf("unknown pin mode %q; expected off, tofu or strict", s)
}

// Store persists pins per host.
type Store struct {
	path string

	mu   sync.Mutex
	pins map[string][]string
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("pin store path must not be empty")
	}
	s := &Store{path: path, pins: map[string][]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.pins); err != nil {
		return nil, fmt.Errorf("parse pin store %s: %w", path, err)
	}
	if err := validatePins(s.pins); err != nil {
		return nil, fmt.Errorf("validate pin store %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Get(host string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.pins[host])
}

func (s *Store) Set(host string, values []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validatePinEntry(host, values); err != nil {
		return err
	}
	sorted := slices.Clone(values)
	sort.Strings(sorted)
	next := clonePins(s.pins)
	next[host] = sorted
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(s.path, append(data, '\n'), 0o600); err != nil {
		return err
	}
	s.pins = next
	return nil
}

// SPKI returns the pin value for a certificate: base64 of the SHA-256 of its
// SubjectPublicKeyInfo, the same construction HPKP used.
func SPKI(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// MismatchError reports a chain that validated normally but matched no pin.
type MismatchError struct {
	Host     string
	Expected []string
	Got      []string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf(
		"certificate pin mismatch for %s: none of the presented keys %v match the stored pins %v; "+
			"if the operator legitimately rotated keys, remove the entry from the pin store and reconnect",
		e.Host, e.Got, e.Expected)
}

// Verifier returns a crypto/tls VerifyPeerCertificate callback for host.
//
// It receives chains that already passed normal verification, so it only has to
// decide whether the chain is the one this client has committed to.
func (s *Store) Verifier(mode Mode, host string) func([][]byte, [][]*x509.Certificate) error {
	if mode == ModeOff {
		return nil
	}
	return func(_ [][]byte, verifiedChains [][]*x509.Certificate) error {
		present := chainPins(verifiedChains)
		if len(present) == 0 {
			return errors.New("no verified certificate chain to pin against")
		}

		stored := s.Get(host)
		if len(stored) == 0 {
			if mode == ModeStrict {
				return fmt.Errorf("no pin stored for %s and strict pinning is on; "+
					"connect once in tofu mode to record one", host)
			}
			return s.Set(host, present)
		}

		for _, p := range present {
			if slices.Contains(stored, p) {
				return nil
			}
		}
		return &MismatchError{Host: host, Expected: stored, Got: present}
	}
}

func chainPins(chains [][]*x509.Certificate) []string {
	var out []string
	for _, chain := range chains {
		for _, cert := range chain {
			if pin := SPKI(cert); !slices.Contains(out, pin) {
				out = append(out, pin)
			}
		}
	}
	return out
}

func validatePins(pins map[string][]string) error {
	for host, values := range pins {
		if err := validatePinEntry(host, values); err != nil {
			return err
		}
	}
	return nil
}

func validatePinEntry(host string, values []string) error {
	if strings.TrimSpace(host) == "" {
		return errors.New("pin host must not be empty")
	}
	if len(values) == 0 {
		return fmt.Errorf("pin list for %s must not be empty", host)
	}
	for _, value := range values {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size {
			return fmt.Errorf("invalid SPKI SHA-256 pin for %s", host)
		}
	}
	return nil
}

func clonePins(source map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(source))
	for host, values := range source {
		cloned[host] = slices.Clone(values)
	}
	return cloned
}
