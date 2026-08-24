package inventory

import (
	"encoding/base64"
	"strings"
	"testing"
)

func key32() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func sample() *Inventory {
	raw := base64.RawURLEncoding.EncodeToString(key32())
	return &Inventory{
		ClientID:    "00000000-0000-4000-8000-000000000000",
		RootDomains: []string{"a.example", "b.example"},
		Hosts: []Host{
			{Slug: "off-01", Name: "offline", Key: raw, Online: false},
			{Slug: "tyo-01", Name: "tokyo", Key: raw, Online: true},
		},
		EntryGroups: []EntryGroup{
			{Name: "南方电信", IPv4: []string{"192.0.2.1"},
				FrontProxy: &FrontProxy{Endpoint: "http://198.51.100.1:443"}},
			{Name: "东方联通", IPv4: []string{"192.0.2.2"}},
		},
	}
}

func TestNormalizedKeyAcceptsEveryBase64Variant(t *testing.T) {
	want := base64.RawURLEncoding.EncodeToString(key32())
	for name, encoded := range map[string]string{
		"rawurl": base64.RawURLEncoding.EncodeToString(key32()),
		"url":    base64.URLEncoding.EncodeToString(key32()),
		"rawstd": base64.RawStdEncoding.EncodeToString(key32()),
		"std":    base64.StdEncoding.EncodeToString(key32()),
	} {
		got, err := Host{Key: encoded}.NormalizedKey()
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if got != want {
			t.Errorf("%s: got %q want %q", name, got, want)
		}
	}
}

func TestNormalizedKeyRejectsWrongLength(t *testing.T) {
	short := base64.RawURLEncoding.EncodeToString([]byte("too short"))
	if _, err := (Host{Key: short}).NormalizedKey(); err == nil {
		t.Fatal("expected an error for a key that is not 32 bytes")
	}
}

func TestSelectHostDefaultsToFirstOnline(t *testing.T) {
	h, err := sample().SelectHost("")
	if err != nil {
		t.Fatal(err)
	}
	if h.Slug != "tyo-01" {
		t.Errorf("got %q, want the first online host", h.Slug)
	}
}

func TestSelectHostBySlugFindsOfflineToo(t *testing.T) {
	h, err := sample().SelectHost("off-01")
	if err != nil {
		t.Fatal(err)
	}
	if h.Slug != "off-01" {
		t.Errorf("got %q", h.Slug)
	}
	if _, err := sample().SelectHost("nope"); err == nil {
		t.Error("expected an error for an unknown slug")
	}
}

func TestSelectGroupMatchesSubstring(t *testing.T) {
	g, err := sample().SelectGroup("联通")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.Name, "联通") {
		t.Errorf("got %q", g.Name)
	}
	if _, err := sample().SelectGroup("nope"); err == nil {
		t.Error("expected an error for an unknown group")
	}
}

func TestSelectRootPrefersPinnedThenCached(t *testing.T) {
	inv := sample()

	if got, err := inv.SelectRoot("b.example", "a.example"); err != nil || got != "b.example" {
		t.Fatalf("pinned should win: got %q err %v", got, err)
	}
	if _, err := inv.SelectRoot("not.in.pool", ""); err == nil {
		t.Error("pinning a root outside the pool should fail")
	}
	if got, err := inv.SelectRoot("", "a.example"); err != nil || got != "a.example" {
		t.Fatalf("cached should be reused: got %q err %v", got, err)
	}
	// A cached root that has rotated out of the pool must not be reused.
	got, err := inv.SelectRoot("", "gone.example")
	if err != nil {
		t.Fatal(err)
	}
	if got == "gone.example" {
		t.Error("a root that left the pool was reused")
	}
}

func TestValidateRejectsIncompleteInventories(t *testing.T) {
	for name, mutate := range map[string]func(*Inventory){
		"no client id": func(i *Inventory) { i.ClientID = "" },
		"no roots":     func(i *Inventory) { i.RootDomains = nil },
		"no hosts":     func(i *Inventory) { i.Hosts = nil },
		"no groups":    func(i *Inventory) { i.EntryGroups = nil },
		"empty pool":   func(i *Inventory) { i.EntryGroups[0].IPv4 = nil },
		"bad host key": func(i *Inventory) { i.Hosts[0].Key = "!!!" },
		"empty slug":   func(i *Inventory) { i.Hosts[0].Slug = "" },
	} {
		inv := sample()
		mutate(inv)
		if err := inv.Validate(); err == nil {
			t.Errorf("%s: expected validation to fail", name)
		}
	}
	if err := sample().Validate(); err != nil {
		t.Fatalf("the sample inventory should be valid: %v", err)
	}
}

func TestFillMissingFromCarriesForwardOnlyAbsentFields(t *testing.T) {
	prev := sample()
	partial := &Inventory{
		ClientID: "kept",
		Hosts:    []Host{{Slug: "only", Key: prev.Hosts[0].Key, Online: true}},
	}
	partial.FillMissingFrom(prev)

	if partial.ClientID != "kept" {
		t.Errorf("an existing client id was overwritten: %q", partial.ClientID)
	}
	if len(partial.Hosts) != 1 || partial.Hosts[0].Slug != "only" {
		t.Errorf("existing hosts were overwritten: %+v", partial.Hosts)
	}
	if len(partial.RootDomains) != len(prev.RootDomains) {
		t.Errorf("root domains were not carried forward: %v", partial.RootDomains)
	}
	if len(partial.EntryGroups) != len(prev.EntryGroups) {
		t.Errorf("entry groups were not carried forward: %v", partial.EntryGroups)
	}
}

func TestFillMissingFromToleratesNoPrevious(t *testing.T) {
	inv := &Inventory{ClientID: "x"}
	inv.FillMissingFrom(nil)
	if inv.ClientID != "x" {
		t.Error("a nil previous inventory should be a no-op")
	}
}
