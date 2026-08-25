package xcfg

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xtls/xray-core/common/platform"
)

func rules(t *testing.T, o Options) []map[string]any {
	t.Helper()
	cfg := build(t, o)
	routing := cfg["routing"].(map[string]any)
	raw := routing["rules"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestParseRouteModeRejectsUnknownValues(t *testing.T) {
	for _, ok := range []string{"global", "smart"} {
		if _, err := ParseRouteMode(ok); err != nil {
			t.Errorf("ParseRouteMode(%q): %v", ok, err)
		}
	}
	if _, err := ParseRouteMode("direct"); err == nil {
		t.Error("an unknown mode should be rejected")
	}
}

// Rules are evaluated in order, so anything that should bypass the tunnel has
// to come before the catch-all that hands traffic to the balancer.
func TestTheCatchAllRuleComesLast(t *testing.T) {
	for _, mode := range []RouteMode{RouteGlobal, RouteSmart} {
		o := baseOptions()
		o.RouteMode = mode
		got := rules(t, o)

		last := got[len(got)-1]
		if last["balancerTag"] != balancerTag {
			t.Errorf("%s: the last rule is %v, want the balancer catch-all", mode, last)
		}
		for i, r := range got[:len(got)-1] {
			if r["outboundTag"] != directTag {
				t.Errorf("%s: rule %d should send traffic direct, got %v", mode, i, r)
			}
		}
	}
}

// The default mode must not depend on any data file, so the bypass list is
// spelled out rather than referring to geoip:private.
func TestGlobalModeNeedsNoRuleSets(t *testing.T) {
	o := baseOptions()
	o.RouteMode = RouteGlobal
	for _, r := range rules(t, o) {
		for _, key := range []string{"ip", "domain"} {
			values, ok := r[key].([]any)
			if !ok {
				continue
			}
			for _, v := range values {
				if s, _ := v.(string); s == "geoip:cn" || s == "geosite:cn" || s == "geoip:private" {
					t.Errorf("global mode referenced the rule set %q", s)
				}
			}
		}
	}
}

func TestSmartModeAddsBothChinaRuleSets(t *testing.T) {
	o := baseOptions()
	o.RouteMode = RouteSmart
	var seen []string
	for _, r := range rules(t, o) {
		for _, key := range []string{"ip", "domain"} {
			values, _ := r[key].([]any)
			for _, v := range values {
				if s, _ := v.(string); s == "geoip:cn" || s == "geosite:cn" {
					seen = append(seen, s)
				}
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("got %v, want both geosite:cn and geoip:cn", seen)
	}
	// A socks5h client hands over a name, and under AsIs a name never matches
	// an IP rule, so the domain set has to be consulted first.
	if seen[0] != "geosite:cn" {
		t.Errorf("rule order is %v; the domain set must come first", seen)
	}
}

func TestADirectOutboundAlwaysExists(t *testing.T) {
	o := baseOptions()
	var tags []string
	for _, ob := range outbounds(t, build(t, o)) {
		tags = append(tags, ob["tag"].(string))
	}
	if tags[len(tags)-1] != directTag {
		t.Errorf("outbounds are %v, want one tagged %q", tags, directTag)
	}
}

func TestDomainStrategyIsOmittedUnlessAsked(t *testing.T) {
	routing := build(t, baseOptions())["routing"].(map[string]any)
	if _, ok := routing["domainStrategy"]; ok {
		t.Error("no strategy was requested, so none should be emitted")
	}

	o := baseOptions()
	o.DomainStrategy = "IPIfNonMatch"
	routing = build(t, o)["routing"].(map[string]any)
	if routing["domainStrategy"] != "IPIfNonMatch" {
		t.Errorf("got %v", routing["domainStrategy"])
	}
}

func TestResolveAssetsReportsWhatIsMissing(t *testing.T) {
	t.Setenv(platform.AssetLocation, "")
	dir := t.TempDir()

	_, err := ResolveAssets(dir)
	if err == nil {
		t.Fatal("an empty directory should be reported as missing rule sets")
	}
	for _, name := range GeoFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ResolveAssets(dir); err != nil {
		t.Errorf("both files are present now: %v", err)
	}
}
