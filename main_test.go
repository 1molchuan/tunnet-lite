package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/1molchuan/tunnet-lite/internal/pinning"
)

func TestBuildHardeningDefaultsToAllProtections(t *testing.T) {
	pinsPath := filepath.Join(t.TempDir(), "pins.json")
	h, err := buildHardening("", true, "tofu", pinsPath)
	if err != nil {
		t.Fatal(err)
	}
	if h.Resolver == nil || !h.ECH || h.Pins == nil || h.PinMode != pinning.ModeTOFU {
		t.Fatalf("incomplete default hardening: %+v", h)
	}
}

func TestBuildHardeningRequiresExplicitECHOptOut(t *testing.T) {
	pinsPath := filepath.Join(t.TempDir(), "pins.json")
	_, err := buildHardening("off", true, "tofu", pinsPath)
	if err == nil || !strings.Contains(err.Error(), "-ech=false") {
		t.Fatalf("expected explicit ECH opt-out error, got %v", err)
	}
}

func TestBuildHardeningAllowsExplicitDNSAndECHOptOut(t *testing.T) {
	h, err := buildHardening("off", false, "off", "unused")
	if err != nil {
		t.Fatal(err)
	}
	if h.Resolver != nil || h.ECH || h.Pins != nil || h.PinMode != pinning.ModeOff {
		t.Fatalf("unexpected hardening configuration: %+v", h)
	}
}

func TestBuildHardeningRejectsNamedDoHUpstream(t *testing.T) {
	_, err := buildHardening("https://resolver.example/dns-query", false, "off", "unused")
	if err == nil {
		t.Fatal("named DoH upstream was accepted")
	}
}
