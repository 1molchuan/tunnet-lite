package pinning

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const pinHost = "control.example"

func testCertificate(key string) *x509.Certificate {
	return &x509.Certificate{RawSubjectPublicKeyInfo: []byte(key)}
}

func verifiedChain(cert *x509.Certificate) [][]*x509.Certificate {
	return [][]*x509.Certificate{{cert}}
}

func TestTOFURecordsPersistsAndMatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	cert := testCertificate("first key")
	if err := store.Verifier(ModeTOFU, pinHost)(nil, verifiedChain(cert)); err != nil {
		t.Fatalf("first use failed: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(reopened.Get(pinHost), SPKI(cert)) {
		t.Fatal("recorded pin was not persisted")
	}
	if err := reopened.Verifier(ModeStrict, pinHost)(nil, verifiedChain(cert)); err != nil {
		t.Fatalf("stored pin did not match: %v", err)
	}
}

func TestVerifierRejectsMismatch(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(pinHost, []string{SPKI(testCertificate("expected"))}); err != nil {
		t.Fatal(err)
	}
	err = store.Verifier(ModeStrict, pinHost)(nil, verifiedChain(testCertificate("unexpected")))
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected MismatchError, got %v", err)
	}
}

func TestStrictModeRejectsMissingPin(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatal(err)
	}
	err = store.Verifier(ModeStrict, pinHost)(nil, verifiedChain(testCertificate("key")))
	if err == nil {
		t.Fatal("strict mode accepted a host with no stored pin")
	}
}

func TestOpenRejectsInvalidPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	data, err := json.Marshal(map[string][]string{pinHost: {"not-a-pin"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("invalid pin store was accepted")
	}
}
