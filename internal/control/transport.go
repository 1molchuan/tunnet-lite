package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"

	"github.com/1molchuan/tunnet-lite/internal/pinning"
)

// Hardening configures how the control endpoint is reached.
//
// The control plane is this client's only trust anchor: the node directory it
// returns is what every later connection is authenticated against. HPKE keeps
// that directory confidential but, being base mode, does not authenticate the
// sender — so the transport below is what decides whether the directory is
// genuine.
type Hardening struct {
	// Resolver resolves the endpoint host over DoH instead of the system
	// resolver. Nil falls back to the system resolver.
	Resolver EndpointResolver
	// ECH hides the endpoint hostname from anyone watching the TLS handshake,
	// using the ECHConfigList published in the host's HTTPS record.
	ECH bool
	// Pins, when set, additionally requires the chain to match a stored pin.
	Pins    CertificatePinStore
	PinMode pinning.Mode
}

// EndpointResolver supplies control-plane addresses and ECH configuration
// without consulting the system resolver.
type EndpointResolver interface {
	LookupIP(context.Context, string) ([]net.IP, error)
	LookupECH(context.Context, string) ([]byte, error)
}

// CertificatePinStore adds a pin check after normal certificate validation.
type CertificatePinStore interface {
	Verifier(pinning.Mode, string) func([][]byte, [][]*x509.Certificate) error
}

// NewHardenedClient builds an HTTP client for the given control endpoint.
func NewHardenedClient(ctx context.Context, endpoint string, h Hardening) (*http.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("bad control endpoint %q: %w", endpoint, err)
	}
	if u.Scheme != "https" || u.User != nil {
		return nil, fmt.Errorf("bad control endpoint %q: HTTPS without user info is required", endpoint)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return nil, fmt.Errorf("bad control endpoint %q: no host", endpoint)
	}

	tlsConfig := &tls.Config{ServerName: host}

	if h.Pins != nil && h.PinMode != pinning.ModeOff {
		tlsConfig.VerifyPeerCertificate = h.Pins.Verifier(h.PinMode, host)
	}

	if err := configureECH(ctx, host, h, tlsConfig); err != nil {
		return nil, err
	}

	transport := &http.Transport{
		TLSClientConfig:     tlsConfig,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	dial := (&net.Dialer{Timeout: 10 * time.Second}).DialContext
	if h.Resolver != nil {
		dial = dialViaResolver(h.Resolver)
	}
	transport.DialContext = dial

	if len(tlsConfig.EncryptedClientHelloConfigList) > 0 {
		if err := useECHDialer(transport, tlsConfig, dial); err != nil {
			return nil, err
		}
	}

	return &http.Client{Timeout: 30 * time.Second, Transport: transport}, nil
}

// useECHDialer takes over the TLS handshake so a rejected ECH attempt can be
// retried.
//
// ECH configurations rotate. A client holding a stale one is refused and handed
// the current list in the same error; recovering from that is part of using
// ECH, not an optional extra. Without it the client simply stops working
// whenever the operator rotates keys.
func useECHDialer(transport *http.Transport, tlsConfig *tls.Config, dial dialFunc) error {
	tlsConfig.NextProtos = []string{"h2", "http/1.1"}
	d := &echDialer{tlsConfig: tlsConfig, dial: dial, config: tlsConfig.EncryptedClientHelloConfigList}

	transport.DialContext = nil
	transport.DialTLSContext = d.dialTLS
	// A custom DialTLSContext suppresses the automatic HTTP/2 upgrade, so
	// register it explicitly; otherwise negotiating h2 leaves the connection
	// unusable.
	return http2.ConfigureTransport(transport)
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type echDialer struct {
	tlsConfig *tls.Config
	dial      dialFunc

	mu     sync.Mutex
	config []byte
}

func (d *echDialer) current() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.config
}

func (d *echDialer) adopt(config []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.config = config
}

func (d *echDialer) dialTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, retry, err := d.attempt(ctx, network, addr, d.current())
	if err == nil {
		return conn, nil
	}
	if retry == nil {
		return nil, err
	}

	// The server supplied its current configuration; adopt it and try once
	// more. A second rejection is a real failure, not a rotation.
	d.adopt(retry)
	conn, _, err = d.attempt(ctx, network, addr, retry)
	return conn, err
}

// attempt returns the server's retry configuration when ECH was rejected
// because the offered one was stale.
func (d *echDialer) attempt(ctx context.Context, network, addr string, echConfig []byte) (net.Conn, []byte, error) {
	raw, err := d.dial(ctx, network, addr)
	if err != nil {
		return nil, nil, err
	}

	cfg := d.tlsConfig.Clone()
	cfg.EncryptedClientHelloConfigList = echConfig
	conn := tls.Client(raw, cfg)
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		var rejected *tls.ECHRejectionError
		if errors.As(err, &rejected) && len(rejected.RetryConfigList) > 0 {
			return nil, rejected.RetryConfigList, err
		}
		return nil, nil, err
	}
	return conn, nil, nil
}

func configureECH(ctx context.Context, host string, h Hardening, tlsConfig *tls.Config) error {
	if !h.ECH {
		return nil
	}
	if h.Resolver == nil {
		return errors.New("ECH is enabled but no DoH resolver is configured")
	}
	echConfig, err := h.Resolver.LookupECH(ctx, host)
	if err != nil {
		return fmt.Errorf("fetch ECH config for %s: %w", host, err)
	}
	if len(echConfig) == 0 {
		return fmt.Errorf("ECH is required but %s publishes no ECH config", host)
	}
	tlsConfig.EncryptedClientHelloConfigList = echConfig
	tlsConfig.MinVersion = tls.VersionTLS13
	tlsConfig.VerifyConnection = requireECH
	return nil
}

func requireECH(state tls.ConnectionState) error {
	if !state.ECHAccepted {
		return errors.New("ECH is required but was not accepted by the control endpoint")
	}
	return nil
}

// dialViaResolver keeps the control hostname out of the system resolver, so it
// does not appear in local DNS logs or reach the network's own recursor.
func dialViaResolver(res EndpointResolver) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		if net.ParseIP(host) != nil {
			return dialer.DialContext(ctx, network, addr)
		}

		ips, err := res.LookupIP(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("no reachable address for %s: %w", host, lastErr)
	}
}
