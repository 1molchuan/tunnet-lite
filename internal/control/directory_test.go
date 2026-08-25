package control

import (
	"encoding/base64"
	"fmt"
	"testing"
)

func fakeKey() string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

const hostsAndEntries = `
  "client_id": "00000000-0000-4000-8000-000000000000",
  "hosts": [{"slug":"tyo-01","name":"Tokyo","online":true,"vless_encryption_key":"%s"}],
  "entry_nodes": [{"name":"ingress","ipv4":["192.0.2.1"],
    "front_proxy":{"endpoint":"http://198.51.100.1:443","headers":{"Host":"cdn.example:443"}}}]`

func accessPayload() []byte {
	body := fmt.Sprintf(hostsAndEntries, fakeKey())
	return []byte(`{"bootstrap":{"runtime":{` + body + `,
		"network":{"root_domains":["a.example"]}}}}`)
}

// A sync response returns the runtime at the top level and omits the root
// domain pool, so it cannot be validated on its own.
func syncPayload() []byte {
	return []byte(`{"runtime":{` + fmt.Sprintf(hostsAndEntries, fakeKey()) + `}}`)
}

func TestParseInventoryAcceptsTheAccessEnvelope(t *testing.T) {
	inv, err := ParseInventory(accessPayload())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Hosts) != 1 || inv.Hosts[0].Slug != "tyo-01" {
		t.Errorf("hosts = %+v", inv.Hosts)
	}
	if len(inv.EntryGroups) != 1 || inv.EntryGroups[0].FrontProxy == nil {
		t.Errorf("entry groups = %+v", inv.EntryGroups)
	}
	if len(inv.RootDomains) != 1 {
		t.Errorf("root domains = %v", inv.RootDomains)
	}
}

func TestParseDirectoryAcceptsTheTopLevelSyncEnvelope(t *testing.T) {
	inv, err := ParseDirectory(syncPayload())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Hosts) != 1 {
		t.Fatalf("sync hosts were not decoded: %+v", inv.Hosts)
	}
	if len(inv.RootDomains) != 0 {
		t.Errorf("a sync payload should not carry root domains, got %v", inv.RootDomains)
	}
}

// A partial sync must be validated only after the missing pool is merged in.
func TestSyncPayloadIsOnlyUsableAfterMerging(t *testing.T) {
	if _, err := ParseInventory(syncPayload()); err == nil {
		t.Fatal("a sync payload should not validate on its own")
	}

	full, err := ParseInventory(accessPayload())
	if err != nil {
		t.Fatal(err)
	}
	partial, err := ParseDirectory(syncPayload())
	if err != nil {
		t.Fatal(err)
	}
	partial.FillMissingFrom(full)
	if err := partial.Validate(); err != nil {
		t.Fatalf("merged directory should validate: %v", err)
	}
	if len(partial.RootDomains) != 1 {
		t.Errorf("root domains were not carried forward: %v", partial.RootDomains)
	}
}

func TestParseDirectoryRejectsPayloadWithoutRuntime(t *testing.T) {
	if _, err := ParseDirectory([]byte(`{"state":"ready"}`)); err == nil {
		t.Fatal("expected an error when no runtime section is present")
	}
}

// An approved identity gets the whole directory back from bootstrap, with the
// runtime at the top level and the root-domain pool included. That payload is
// what recovers a client which was approved but never obtained a directory, so
// it has to validate on its own without anything to merge into.
func TestABootstrapPayloadIsUsableOnItsOwn(t *testing.T) {
	body := fmt.Sprintf(hostsAndEntries, fakeKey())
	payload := []byte(`{"schema_version":2,"access":{"state":"ready"},
		"release":{"minimum_version":"0.2.5"},
		"runtime":{` + body + `,"network":{"root_domains":["a.example"]}}}`)

	inv, err := ParseInventory(payload)
	if err != nil {
		t.Fatalf("a bootstrap payload should validate unaided: %v", err)
	}
	if len(inv.Hosts) != 1 || len(inv.EntryGroups) != 1 || len(inv.RootDomains) != 1 {
		t.Errorf("incomplete inventory: %d hosts, %d groups, %d roots",
			len(inv.Hosts), len(inv.EntryGroups), len(inv.RootDomains))
	}
}

// A bootstrap that only asks for approval must not be mistaken for a directory,
// or the client would report success and then have nothing to dial.
func TestAnUnapprovedBootstrapIsNotADirectory(t *testing.T) {
	payload := []byte(`{"access":{"state":"pending","ticket":"abc",
		"verify":"https://access.example/#abc"},"release":{"minimum_version":"0.2.5"}}`)
	if _, err := ParseInventory(payload); err == nil {
		t.Fatal("a payload with no runtime should not parse as a directory")
	}
}
