package control

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Identity is the long-lived client identity. The control plane binds an
// authorisation to this key pair, so persisting it is what lets a client resume
// with a plain sync instead of bootstrapping a fresh identity on every launch.
type Identity struct {
	ClientID   string
	SigningKey ed25519.PrivateKey

	// PendingTicket is an authorisation waiting to be approved. Approval
	// happens in a browser, between one run and the next, so the ticket has to
	// outlive the process that obtained it — otherwise the run that follows
	// approval has no way to exchange it for the directory.
	PendingTicket string
}

type identityFile struct {
	ClientID      string `json:"client_id"`
	SigningKey    string `json:"signing_key"`
	PendingTicket string `json:"pending_ticket,omitempty"`
}

func NewIdentity() (*Identity, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	raw[6] = raw[6]&0x0f | 0x80
	raw[8] = raw[8]&0x3f | 0x80

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{ClientID: formatUUID(raw), SigningKey: priv}, nil
}

func (id *Identity) PublicKey() string {
	return base64.RawURLEncoding.EncodeToString(id.SigningKey.Public().(ed25519.PublicKey))
}

// LoadIdentity reads a stored identity. A missing file is reported as
// os.ErrNotExist so callers can decide whether to bootstrap a new one.
func LoadIdentity(path string) (*Identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f identityFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse identity %s: %w", path, err)
	}
	key, err := base64.RawURLEncoding.DecodeString(f.SigningKey)
	if err != nil {
		return nil, fmt.Errorf("parse identity %s: %w", path, err)
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("stored signing key has the wrong size")
	}
	if f.ClientID == "" {
		return nil, errors.New("stored identity has no client id")
	}
	return &Identity{ClientID: f.ClientID, SigningKey: key, PendingTicket: f.PendingTicket}, nil
}

// Save writes the identity with owner-only permissions. It holds a private key
// and the account identifier, so it must never be committed or shared.
func (id *Identity) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(identityFile{
		ClientID:      id.ClientID,
		SigningKey:    base64.RawURLEncoding.EncodeToString(id.SigningKey),
		PendingTicket: id.PendingTicket,
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func formatUUID(raw []byte) string {
	s := hex.EncodeToString(raw)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
