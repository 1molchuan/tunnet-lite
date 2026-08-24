// Package xcfg renders the Xray configuration that carries the tunnel.
//
// The wire parameters below are not guesses; they were established by sweeping
// them against a live node (see sweep/). Treat any change here as something to
// re-validate with that tool, not as a cosmetic edit.
package xcfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/example/tunnet-lite/internal/inventory"
)

// Wire parameters confirmed against a live node.
//
//   - Mode "native" (xorMode 0). "xorpub" and "random" are both rejected by the
//     server: it closes both HTTP/2 streams right after the 1333-byte hello.
//   - "1rtt" and "0rtt" behave identically on a first connection.
//   - Padding 100-35-35 yields a single 35-byte pad, i.e. a 1333-byte hello.
//   - Flow xtls-rprx-vision is mandatory. Without it the encryption handshake
//     still completes but no payload is ever returned.
const (
	DefaultMode    = "native"
	DefaultRTT     = "1rtt"
	DefaultPadding = "100-35-35"
	DefaultFlow    = "xtls-rprx-vision"

	// XTLS Vision refuses UDP to port 443 unless the flow carries the udp443
	// suffix. The suffix is a client-side switch: the outbound truncates it
	// back to the plain flow name before it reaches the server.
	flowUDP443 = DefaultFlow + "-udp443"

	// FlowNone disables the flow field. An empty Options.Flow means "use the
	// default" so that a zero-valued Options still renders something usable,
	// which leaves no way to express "no flow" without a distinct token.
	FlowNone = "none"

	xhttpPath   = "/api/v1/sync/"
	echResolver = "https://223.5.5.5/dns-query"
)

// Options describes one rendered configuration.
type Options struct {
	Listen string // SOCKS bind address
	Port   int    // SOCKS port

	ClientID      string
	LogicalHost   string // <slug>.<root-domain>
	EncryptionKey string // raw URL base64, 32 bytes decoded

	// Entries are CDN addresses ordered best-first. The first entry becomes the
	// balancer's fallback, which is what carries traffic until the health checks
	// have produced their first samples.
	Entries    []string
	FrontProxy *inventory.FrontProxy

	// UDP enables SOCKS UDP associate. Without it the listener is TCP-only.
	UDP bool

	// Encryption wire parameters. Zero values fall back to the validated
	// defaults above; they are settable so the sweep tool can drive them.
	Mode    string
	RTT     string
	Padding string
	Flow    string

	// Health checking. Interval must exceed Timeout: overlapping rounds cancel
	// each other and every entry, healthy ones included, then reports a closed
	// pipe. Timeout must be generous because each probe builds a complete
	// VLESS Encryption + XHTTP + TLS/ECH session from scratch.
	ProbeURL      string
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration

	LogLevel string
}

func (o *Options) applyDefaults() {
	if o.Mode == "" {
		o.Mode = DefaultMode
	}
	if o.RTT == "" {
		o.RTT = DefaultRTT
	}
	if o.Padding == "" {
		o.Padding = DefaultPadding
	}
	switch o.Flow {
	case "":
		o.Flow = DefaultFlow
	case FlowNone:
		o.Flow = ""
	}
	if o.UDP && o.Flow == DefaultFlow {
		o.Flow = flowUDP443
	}
	if o.ProbeURL == "" {
		o.ProbeURL = "https://connectivitycheck.gstatic.com/generate_204"
	}
	if o.ProbeTimeout <= 0 {
		o.ProbeTimeout = 15 * time.Second
	}
	if o.ProbeInterval <= o.ProbeTimeout {
		o.ProbeInterval = 4 * o.ProbeTimeout
	}
	if o.LogLevel == "" {
		o.LogLevel = "warning"
	}
	if o.Listen == "" {
		o.Listen = "127.0.0.1"
	}
}

func (o Options) validate() error {
	if o.ClientID == "" {
		return errors.New("client id is empty")
	}
	if o.EncryptionKey == "" {
		return errors.New("encryption key is empty")
	}
	if o.LogicalHost == "" {
		return errors.New("logical host is empty")
	}
	if len(o.Entries) == 0 {
		return errors.New("no entry address")
	}
	if o.Port <= 0 || o.Port > 65535 {
		return fmt.Errorf("invalid socks port %d", o.Port)
	}
	return nil
}

// Build renders the configuration as JSON ready for core.LoadConfig.
func Build(o Options) ([]byte, error) {
	o.applyDefaults()
	if err := o.validate(); err != nil {
		return nil, err
	}

	outbounds := make([]any, 0, len(o.Entries)+1)
	tags := make([]string, 0, len(o.Entries))
	for i, ip := range o.Entries {
		tag := "entry-" + strconv.Itoa(i)
		tags = append(tags, tag)
		outbounds = append(outbounds, o.entryOutbound(tag, ip))
	}
	if o.FrontProxy != nil {
		fp, err := frontOutbound(o.FrontProxy)
		if err != nil {
			return nil, err
		}
		outbounds = append(outbounds, fp)
	}

	cfg := map[string]any{
		"log": map[string]any{"loglevel": o.LogLevel},
		"inbounds": []any{map[string]any{
			"tag":      "socks-in",
			"listen":   o.Listen,
			"port":     o.Port,
			"protocol": "socks",
			"settings": map[string]any{"udp": o.UDP},
		}},
		"outbounds": outbounds,
		"routing": map[string]any{
			"rules": []any{map[string]any{
				"type": "field", "network": "tcp,udp", "balancerTag": "entry-pool",
			}},
			"balancers": []any{map[string]any{
				"tag":      "entry-pool",
				"selector": []string{"entry-"},
				"strategy": map[string]any{"type": "leastPing"},
				// Best TCP RTT at startup; carries traffic until health data exists.
				"fallbackTag": tags[0],
			}},
		},
		"burstObservatory": map[string]any{
			"subjectSelector": []string{"entry-"},
			"pingConfig": map[string]any{
				"destination": o.ProbeURL,
				"interval":    durationString(o.ProbeInterval),
				"timeout":     durationString(o.ProbeTimeout),
				"sampling":    1,
			},
		},
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func (o Options) entryOutbound(tag, ip string) map[string]any {
	stream := map[string]any{
		"network":  "xhttp",
		"security": "tls",
		"tlsSettings": map[string]any{
			"serverName":    o.LogicalHost,
			"alpn":          []string{"h2"},
			"fingerprint":   "hellochrome_133",
			"echConfigList": o.LogicalHost + "+" + echResolver,
		},
		"xhttpSettings": map[string]any{
			"host": o.LogicalHost, "path": xhttpPath, "mode": "stream-up",
			"headers":           browserHeaders(),
			"xPaddingBytes":     "100-999",
			"xPaddingObfsMode":  true,
			"xPaddingPlacement": "queryInHeader",
			"xPaddingKey":       "cache",
			"xPaddingHeader":    "Referer",
			"xPaddingMethod":    "tokenish",
			"sessionIDTable":    "Base62",
			"sessionIDLength":   "16-24",
		},
	}
	if o.FrontProxy != nil {
		stream["sockopt"] = map[string]any{"dialerProxy": "front-proxy"}
	}

	settings := map[string]any{
		"address": ip, "port": 443, "id": o.ClientID,
		"encryption": o.encryption(),
	}
	// An empty flow must be omitted entirely rather than sent as "".
	if o.Flow != "" {
		settings["flow"] = o.Flow
	}

	return map[string]any{
		"tag": tag, "protocol": "vless",
		"settings":       settings,
		"streamSettings": stream,
	}
}

func (o Options) encryption() string {
	return strings.Join([]string{
		"mlkem768x25519plus", o.Mode, o.RTT, o.Padding, o.EncryptionKey,
	}, ".")
}

func frontOutbound(fp *inventory.FrontProxy) (map[string]any, error) {
	host, port, err := splitEndpoint(fp.Endpoint)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"tag": "front-proxy", "protocol": "http",
		"settings": map[string]any{
			"servers": []any{map[string]any{"address": host, "port": port}},
			"headers": fp.Headers,
		},
	}, nil
}

func splitEndpoint(endpoint string) (string, int, error) {
	s := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(
		endpoint, "http://"), "https://"), "/")
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		return "", 0, fmt.Errorf("front proxy endpoint %q: %w", endpoint, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, fmt.Errorf("front proxy endpoint %q: %w", endpoint, err)
	}
	return host, port, nil
}

func durationString(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds())) + "s"
}

// browserHeaders is the fixed header set the reference client sends on every
// XHTTP request.
func browserHeaders() map[string]string {
	return map[string]string{
		"Accept":             "*/*",
		"Accept-Language":    "en-US,en;q=0.9",
		"Cache-Control":      "no-cache",
		"Dnt":                "1",
		"Pragma":             "no-cache",
		"Priority":           "u=1, i",
		"Sec-Ch-Ua-Mobile":   "?0",
		"Sec-Ch-Ua-Platform": `"Windows"`,
		"Sec-Fetch-Dest":     "empty",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Site":     "same-origin",
		"Sec-Ch-Ua":          `"Google Chrome";v="133", "Chromium";v="133", "Not_A Brand";v="24"`,
		"User-Agent":         "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
	}
}
