package xcfg

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/1molchuan/tunnet-lite/internal/inventory"
)

func baseOptions() Options {
	return Options{
		Port:          18080,
		ClientID:      "00000000-0000-4000-8000-000000000000",
		LogicalHost:   "tyo-01.a.example",
		EncryptionKey: strings.Repeat("A", 43),
		Entries:       []string{"192.0.2.1", "192.0.2.2"},
	}
}

func build(t *testing.T, o Options) map[string]any {
	t.Helper()
	data, err := Build(o)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("rendered config is not valid JSON: %v", err)
	}
	return cfg
}

func outbounds(t *testing.T, cfg map[string]any) []map[string]any {
	t.Helper()
	raw, _ := cfg["outbounds"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, o := range raw {
		out = append(out, o.(map[string]any))
	}
	return out
}

func TestBuildUsesValidatedWireDefaults(t *testing.T) {
	cfg := build(t, baseOptions())
	first := outbounds(t, cfg)[0]
	settings := first["settings"].(map[string]any)

	wantEnc := "mlkem768x25519plus.native.1rtt.100-35-35." + strings.Repeat("A", 43)
	if got := settings["encryption"]; got != wantEnc {
		t.Errorf("encryption = %v, want %v", got, wantEnc)
	}
	if got := settings["flow"]; got != DefaultFlow {
		t.Errorf("flow = %v, want %v", got, DefaultFlow)
	}
}

func TestBuildOnePoolEntryPerAddressWithFallbackOnTheFirst(t *testing.T) {
	cfg := build(t, baseOptions())

	var tags []string
	for _, o := range outbounds(t, cfg) {
		tags = append(tags, o["tag"].(string))
	}
	want := []string{"entry-0", "entry-1"}
	for i, w := range want {
		if tags[i] != w {
			t.Fatalf("tags = %v, want them to start with %v", tags, want)
		}
	}

	routing := cfg["routing"].(map[string]any)
	balancer := routing["balancers"].([]any)[0].(map[string]any)
	if balancer["fallbackTag"] != "entry-0" {
		t.Errorf("fallbackTag = %v, want the best-ranked entry", balancer["fallbackTag"])
	}
}

func TestFrontProxyAddsAnOutboundAndChainsTheDial(t *testing.T) {
	o := baseOptions()
	o.FrontProxy = &inventory.FrontProxy{
		Endpoint: "http://198.51.100.1:443",
		Headers:  map[string]string{"Host": "cdn.example:443"},
	}
	cfg := build(t, o)
	obs := outbounds(t, cfg)

	last := obs[len(obs)-1]
	if last["tag"] != "front-proxy" {
		t.Fatalf("expected a front-proxy outbound, got %v", last["tag"])
	}
	servers := last["settings"].(map[string]any)["servers"].([]any)
	server := servers[0].(map[string]any)
	if server["address"] != "198.51.100.1" || server["port"].(float64) != 443 {
		t.Errorf("front proxy endpoint was not split correctly: %v", server)
	}

	stream := obs[0]["streamSettings"].(map[string]any)
	sockopt, ok := stream["sockopt"].(map[string]any)
	if !ok || sockopt["dialerProxy"] != "front-proxy" {
		t.Errorf("entry outbound is not chained through the front proxy: %v", stream["sockopt"])
	}
}

func TestWithoutFrontProxyTheDialIsNotChained(t *testing.T) {
	cfg := build(t, baseOptions())
	stream := outbounds(t, cfg)[0]["streamSettings"].(map[string]any)
	if _, ok := stream["sockopt"]; ok {
		t.Error("no front proxy was configured, so no dialerProxy should be set")
	}
}

// Overlapping health check rounds cancel each other and every entry then
// reports a closed pipe, healthy ones included. The interval must stay clear
// of the timeout regardless of what the caller asks for.
func TestHealthIntervalIsForcedAboveTheTimeout(t *testing.T) {
	o := baseOptions()
	o.ProbeTimeout = 15 * time.Second
	o.ProbeInterval = 5 * time.Second
	cfg := build(t, o)

	ping := cfg["burstObservatory"].(map[string]any)["pingConfig"].(map[string]any)
	if ping["interval"] == "5s" {
		t.Fatal("an interval below the timeout was accepted")
	}
	if ping["timeout"] != "15s" {
		t.Errorf("timeout = %v", ping["timeout"])
	}
}

func TestBuildRejectsIncompleteOptions(t *testing.T) {
	for name, mutate := range map[string]func(*Options){
		"no client id": func(o *Options) { o.ClientID = "" },
		"no key":       func(o *Options) { o.EncryptionKey = "" },
		"no host":      func(o *Options) { o.LogicalHost = "" },
		"no entries":   func(o *Options) { o.Entries = nil },
		"bad port":     func(o *Options) { o.Port = 0 },
	} {
		o := baseOptions()
		mutate(&o)
		if _, err := Build(o); err == nil {
			t.Errorf("%s: expected Build to fail", name)
		}
	}
}

func TestSplitEndpointRejectsMalformedValues(t *testing.T) {
	if _, _, err := splitEndpoint("http://no-port"); err == nil {
		t.Error("expected an error for an endpoint without a port")
	}
	host, port, err := splitEndpoint("http://198.51.100.1:443/")
	if err != nil || host != "198.51.100.1" || port != 443 {
		t.Errorf("got %q %d %v", host, port, err)
	}
}

// An empty Flow means "use the default" so a zero-valued Options still renders
// something usable; disabling the field needs its own token.
func TestFlowNoneOmitsTheFieldWhileEmptyKeepsTheDefault(t *testing.T) {
	o := baseOptions()
	o.Flow = FlowNone
	settings := outbounds(t, build(t, o))[0]["settings"].(map[string]any)
	if _, ok := settings["flow"]; ok {
		t.Errorf(`flow should be absent when Flow is %q, got %v`, FlowNone, settings["flow"])
	}

	o = baseOptions()
	o.Flow = ""
	settings = outbounds(t, build(t, o))[0]["settings"].(map[string]any)
	if settings["flow"] != DefaultFlow {
		t.Errorf("empty Flow should fall back to the default, got %v", settings["flow"])
	}
}

// Vision refuses UDP to port 443 unless the flow carries the udp443 suffix.
func TestUDPUpgradesVisionToTheUDP443Variant(t *testing.T) {
	o := baseOptions()
	o.UDP = true
	cfg := build(t, o)

	settings := outbounds(t, cfg)[0]["settings"].(map[string]any)
	if settings["flow"] != DefaultFlow+"-udp443" {
		t.Errorf("flow = %v, want the udp443 variant", settings["flow"])
	}
	inbound := cfg["inbounds"].([]any)[0].(map[string]any)
	if inbound["settings"].(map[string]any)["udp"] != true {
		t.Error("the SOCKS inbound should have UDP enabled")
	}
}

func TestUDPDoesNotResurrectADisabledFlow(t *testing.T) {
	o := baseOptions()
	o.UDP = true
	o.Flow = FlowNone
	settings := outbounds(t, build(t, o))[0]["settings"].(map[string]any)
	if _, ok := settings["flow"]; ok {
		t.Errorf("flow should stay absent, got %v", settings["flow"])
	}
}
