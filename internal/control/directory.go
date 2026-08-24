package control

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/1molchuan/tunnet-lite/internal/inventory"
)

// runtimePayload is the part of the directory this client needs. The payload
// carries more than this; anything not modelled here is ignored.
type runtimePayload struct {
	ClientID   string `json:"client_id"`
	EntryNodes []struct {
		Name       string   `json:"name"`
		IPv4       []string `json:"ipv4"`
		FrontProxy *struct {
			Endpoint string            `json:"endpoint"`
			Headers  map[string]string `json:"headers"`
		} `json:"front_proxy"`
	} `json:"entry_nodes"`
	Hosts []struct {
		Name   string `json:"name"`
		Slug   string `json:"slug"`
		Online bool   `json:"online"`
		Key    string `json:"vless_encryption_key"`
	} `json:"hosts"`
	Network struct {
		RootDomains []string `json:"root_domains"`
	} `json:"network"`
}

// directory covers both envelopes the control plane uses: access nests the
// runtime under "bootstrap", while sync returns it at the top level.
type directory struct {
	Runtime   *runtimePayload `json:"runtime"`
	Bootstrap *struct {
		Runtime *runtimePayload `json:"runtime"`
	} `json:"bootstrap"`
}

func (d directory) runtime() *runtimePayload {
	if d.Bootstrap != nil && d.Bootstrap.Runtime != nil {
		return d.Bootstrap.Runtime
	}
	return d.Runtime
}

// ParseInventory converts a decrypted payload into a validated inventory. Use
// it for a full access response; a sync response is partial and must go through
// ParseDirectory plus FillMissingFrom instead.
func ParseInventory(payload []byte) (*inventory.Inventory, error) {
	inv, err := ParseDirectory(payload)
	if err != nil {
		return nil, err
	}
	if err := inv.Validate(); err != nil {
		return nil, fmt.Errorf("directory is not usable: %w", err)
	}
	return inv, nil
}

// ParseDirectory decodes a payload without validating it, so a partial sync
// response can be merged onto what is already known.
func ParseDirectory(payload []byte) (*inventory.Inventory, error) {
	var d directory
	if err := json.Unmarshal(payload, &d); err != nil {
		return nil, fmt.Errorf("parse directory: %w", err)
	}
	rt := d.runtime()
	if rt == nil {
		return nil, errors.New("directory contains no runtime section")
	}

	inv := &inventory.Inventory{
		ClientID:    rt.ClientID,
		RootDomains: rt.Network.RootDomains,
	}
	for _, h := range rt.Hosts {
		inv.Hosts = append(inv.Hosts, inventory.Host{
			Slug: h.Slug, Name: h.Name, Online: h.Online, Key: h.Key,
		})
	}
	for _, e := range rt.EntryNodes {
		g := inventory.EntryGroup{Name: e.Name, IPv4: e.IPv4}
		if e.FrontProxy != nil {
			g.FrontProxy = &inventory.FrontProxy{
				Endpoint: e.FrontProxy.Endpoint,
				Headers:  e.FrontProxy.Headers,
			}
		}
		inv.EntryGroups = append(inv.EntryGroups, g)
	}
	return inv, nil
}
