// Package inventory loads and validates the node inventory that tunnet-lite
// dials. Nothing here is hardcoded: the client identity, the per-host VLESS
// Encryption keys, the rotating root domains and the operator entry groups all
// come from the inventory file supplied at run time.
package inventory

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
)

// FrontProxy is a domestic HTTP CONNECT hop placed in front of the CDN entry.
// Headers carries the exact header set the proxy requires; both the Host
// override and the auth header are mandatory for the tunnel to be established.
type FrontProxy struct {
	Endpoint string            `json:"endpoint"`
	Headers  map[string]string `json:"headers"`
}

// EntryGroup is one operator ingress: a pool of CDN addresses reachable through
// a shared front proxy. Groups differ by front proxy, not necessarily by pool.
type EntryGroup struct {
	Name       string      `json:"name"`
	IPv4       []string    `json:"ipv4"`
	FrontProxy *FrontProxy `json:"front_proxy,omitempty"`
}

// Host is a logical exit. Key is the host's 32-byte X25519 VLESS Encryption
// public key; it is per-host, so switching exits also switches keys.
type Host struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Key    string `json:"vless_encryption_key"`
	Online bool   `json:"online"`
}

type Inventory struct {
	ClientID    string       `json:"client_id"`
	RootDomains []string     `json:"root_domains"`
	Hosts       []Host       `json:"hosts"`
	EntryGroups []EntryGroup `json:"entry_groups"`
}

// FillMissingFrom carries forward fields that a partial update does not
// contain. The control plane only ships the root-domain pool with a full access
// response; a sync refreshes hosts and entry pools but omits it, so the pool
// has to survive across refreshes.
func (inv *Inventory) FillMissingFrom(prev *Inventory) {
	if prev == nil {
		return
	}
	if len(inv.RootDomains) == 0 {
		inv.RootDomains = prev.RootDomains
	}
	if inv.ClientID == "" {
		inv.ClientID = prev.ClientID
	}
	if len(inv.Hosts) == 0 {
		inv.Hosts = prev.Hosts
	}
	if len(inv.EntryGroups) == 0 {
		inv.EntryGroups = prev.EntryGroups
	}
}

func Load(path string) (*Inventory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read inventory: %w", err)
	}
	var inv Inventory
	if err := json.Unmarshal(data, &inv); err != nil {
		return nil, fmt.Errorf("parse inventory %s: %w", path, err)
	}
	if err := inv.Validate(); err != nil {
		return nil, fmt.Errorf("invalid inventory %s: %w", path, err)
	}
	return &inv, nil
}

func (inv *Inventory) Validate() error {
	if inv.ClientID == "" {
		return errors.New("client_id is empty")
	}
	if len(inv.RootDomains) == 0 {
		return errors.New("root_domains is empty")
	}
	if len(inv.Hosts) == 0 {
		return errors.New("hosts is empty")
	}
	if len(inv.EntryGroups) == 0 {
		return errors.New("entry_groups is empty")
	}
	for _, h := range inv.Hosts {
		if h.Slug == "" {
			return errors.New("a host has an empty slug")
		}
		if _, err := h.NormalizedKey(); err != nil {
			return fmt.Errorf("host %s: %w", h.Slug, err)
		}
	}
	for _, g := range inv.EntryGroups {
		if len(g.IPv4) == 0 {
			return fmt.Errorf("entry group %q has no address", g.Name)
		}
	}
	return nil
}

// NormalizedKey decodes the key from any common base64 variant and re-encodes
// it as raw URL base64, which is the form Xray's "encryption" string expects.
func (h Host) NormalizedKey() (string, error) {
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding,
		base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(h.Key); err == nil && len(b) == 32 {
			return base64.RawURLEncoding.EncodeToString(b), nil
		}
	}
	return "", errors.New("vless_encryption_key is not a 32-byte base64 value")
}

// SelectHost returns the named exit, or the first online one when slug is empty.
func (inv *Inventory) SelectHost(slug string) (Host, error) {
	for _, h := range inv.Hosts {
		if slug != "" && h.Slug == slug {
			return h, nil
		}
		if slug == "" && h.Online {
			return h, nil
		}
	}
	if slug == "" {
		return Host{}, errors.New("no online host in inventory")
	}
	return Host{}, fmt.Errorf("no host with slug %q", slug)
}

// SelectGroup returns the named entry group, or the first one when name is empty.
// Matching is case-insensitive and accepts a substring, so partial names work.
func (inv *Inventory) SelectGroup(name string) (EntryGroup, error) {
	if name == "" {
		return inv.EntryGroups[0], nil
	}
	for _, g := range inv.EntryGroups {
		if strings.EqualFold(g.Name, name) || strings.Contains(g.Name, name) {
			return g, nil
		}
	}
	return EntryGroup{}, fmt.Errorf("no entry group matching %q", name)
}

// SelectRoot mirrors the reference client: a root domain is chosen once and
// then reused for as long as it stays in the pool, so a client keeps hitting
// the same <slug>.<root> instead of rotating on every connection. Passing a
// pinned value overrides the cached choice.
func (inv *Inventory) SelectRoot(pinned, cached string) (string, error) {
	if pinned != "" {
		if !slices.Contains(inv.RootDomains, pinned) {
			return "", fmt.Errorf("root domain %q is not in the inventory pool", pinned)
		}
		return pinned, nil
	}
	if cached != "" && slices.Contains(inv.RootDomains, cached) {
		return cached, nil
	}
	return inv.RootDomains[rand.N(len(inv.RootDomains))], nil
}
