package control

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type transportTestResolver struct {
	ech []byte
	err error
}

func (r transportTestResolver) LookupIP(context.Context, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("192.0.2.1")}, nil
}

func (r transportTestResolver) LookupECH(context.Context, string) ([]byte, error) {
	return r.ech, r.err
}

func TestNewHardenedClientRequiresResolverForECH(t *testing.T) {
	_, err := NewHardenedClient(context.Background(), "https://example.com/api", Hardening{ECH: true})
	if err == nil || !strings.Contains(err.Error(), "no DoH resolver") {
		t.Fatalf("expected missing resolver error, got %v", err)
	}
}

func TestNewHardenedClientRejectsInsecureEndpoint(t *testing.T) {
	_, err := NewHardenedClient(context.Background(), "http://example.com/api", Hardening{})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("expected HTTPS endpoint error, got %v", err)
	}
}

func TestNewHardenedClientFailsWithoutECHConfig(t *testing.T) {
	h := Hardening{ECH: true, Resolver: transportTestResolver{}}
	_, err := NewHardenedClient(context.Background(), "https://example.com/api", h)
	if err == nil || !strings.Contains(err.Error(), "publishes no ECH config") {
		t.Fatalf("expected missing ECH config error, got %v", err)
	}
}

func TestNewHardenedClientPropagatesECHLookupFailure(t *testing.T) {
	lookupErr := errors.New("resolver unavailable")
	h := Hardening{ECH: true, Resolver: transportTestResolver{err: lookupErr}}
	_, err := NewHardenedClient(context.Background(), "https://example.com/api", h)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("expected lookup error, got %v", err)
	}
}

func TestNewHardenedClientRequiresAcceptedECH(t *testing.T) {
	echConfig := []byte{1, 2, 3}
	h := Hardening{ECH: true, Resolver: transportTestResolver{ech: echConfig}}
	client, err := NewHardenedClient(context.Background(), "https://example.com/api", h)
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	tlsConfig := transport.TLSClientConfig
	if !reflect.DeepEqual(tlsConfig.EncryptedClientHelloConfigList, echConfig) {
		t.Fatalf("ECH config = %v, want %v", tlsConfig.EncryptedClientHelloConfigList, echConfig)
	}
	if err := tlsConfig.VerifyConnection(tls.ConnectionState{}); err == nil {
		t.Fatal("expected a rejected ECH handshake to fail")
	}
	if err := tlsConfig.VerifyConnection(tls.ConnectionState{ECHAccepted: true}); err != nil {
		t.Fatalf("accepted ECH handshake failed: %v", err)
	}
}
