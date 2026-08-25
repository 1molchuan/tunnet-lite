package xcfg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xtls/xray-core/common/platform"
)

// Routing decides which traffic leaves through the tunnel and which is sent
// straight out.
type RouteMode string

const (
	// RouteGlobal sends everything through the tunnel except addresses that
	// cannot meaningfully be proxied.
	RouteGlobal RouteMode = "global"
	// RouteSmart additionally keeps mainland-China destinations local, which
	// is what the vendor client does with its own embedded copy of the same
	// rule sets.
	RouteSmart RouteMode = "smart"
)

func ParseRouteMode(s string) (RouteMode, error) {
	switch RouteMode(s) {
	case RouteGlobal, RouteSmart:
		return RouteMode(s), nil
	}
	return "", fmt.Errorf("unknown route mode %q; expected global or smart", s)
}

// GeoFiles are the rule sets smart routing needs. They are deliberately not
// embedded: keeping them as ordinary files means you can see which rules are in
// force and replace them without rebuilding.
var GeoFiles = []string{"geoip.dat", "geosite.dat"}

// directPrefixes are the addresses that never go through a tunnel: private,
// loopback, link-local, carrier NAT, multicast, reserved and documentation
// ranges. They are listed literally rather than as geoip:private so that the
// default mode needs no data files at all.
var directPrefixes = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4",
	"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "2001:db8::/32",
}

// ResolveAssets points xray-core at the directory holding the rule sets and
// reports whether both files are present.
//
// Xray looks the files up through a process-wide setting, so this has to be
// applied before the instance starts.
func ResolveAssets(dir string) (string, error) {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		if err := os.Setenv(platform.AssetLocation, abs); err != nil {
			return "", err
		}
	}

	var missing []string
	var found string
	for _, name := range GeoFiles {
		path := platform.GetAssetLocation(name)
		found = filepath.Dir(path)
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return found, fmt.Errorf("missing rule set %s in %s", strings.Join(missing, " and "), found)
	}
	return found, nil
}

// routingRules builds the rule list, most specific first. Xray evaluates rules
// in order, so the direct cases have to precede the catch-all.
func (o Options) routingRules() []any {
	rules := []any{
		map[string]any{
			"type": "field", "ip": directPrefixes, "outboundTag": directTag,
		},
	}

	if o.RouteMode == RouteSmart {
		rules = append(rules,
			// Domains first: a SOCKS client using socks5h hands over a name,
			// and a name never matches an IP rule under the AsIs strategy.
			map[string]any{
				"type": "field", "domain": []string{"geosite:cn"}, "outboundTag": directTag,
			},
			map[string]any{
				"type": "field", "ip": []string{"geoip:cn"}, "outboundTag": directTag,
			},
		)
	}

	return append(rules, map[string]any{
		"type": "field", "network": "tcp,udp", "balancerTag": balancerTag,
	})
}

func directOutbound() map[string]any {
	return map[string]any{
		"tag": directTag, "protocol": "freedom",
		// Names that route directly are resolved locally, which is the point:
		// the destination is local, so the lookup should be too.
		"settings": map[string]any{"domainStrategy": "UseIP"},
	}
}
