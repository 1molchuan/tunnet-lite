package resolver

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

var testECHConfig = []byte{0, 5, 1, 2, 3, 4, 5}

func TestLookupIPAndECH(t *testing.T) {
	server := httptest.NewTLSServer(dnsHandler(t))
	defer server.Close()
	resolver := testResolver(t, server)

	ips, err := resolver.LookupIP(context.Background(), "control.example")
	if err != nil {
		t.Fatal(err)
	}
	if !containsIP(ips, "192.0.2.10") || !containsIP(ips, "2001:db8::10") {
		t.Fatalf("unexpected addresses: %v", ips)
	}

	ech, err := resolver.LookupECH(context.Background(), "control.example")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ech, testECHConfig) {
		t.Fatalf("ECH config = %v, want %v", ech, testECHConfig)
	}
}

func TestLookupFallsBackToSecondUpstream(t *testing.T) {
	failing := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	working := httptest.NewTLSServer(dnsHandler(t))
	defer working.Close()

	resolver, err := NewDoH(failing.URL, working.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolver.HTTP = working.Client()
	if _, err := resolver.LookupECH(context.Background(), "control.example"); err != nil {
		t.Fatalf("second upstream was not used: %v", err)
	}
}

func TestNewDoHRejectsNamedOrInsecureUpstream(t *testing.T) {
	for _, upstream := range []string{
		"https://resolver.example/dns-query",
		"http://192.0.2.1/dns-query",
		"not a URL",
	} {
		if _, err := NewDoH(upstream); err == nil {
			t.Errorf("accepted unsafe upstream %q", upstream)
		}
	}
}

func TestExchangeRejectsWrongContentType(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("not DNS"))
	}))
	defer server.Close()
	resolver := testResolver(t, server)
	_, err := resolver.LookupECH(context.Background(), "control.example")
	if err == nil || !strings.Contains(err.Error(), "invalid content type") {
		t.Fatalf("expected content-type error, got %v", err)
	}
}

func testResolver(t *testing.T, server *httptest.Server) *DoH {
	t.Helper()
	resolver, err := NewDoH(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resolver.HTTP = server.Client()
	return resolver
}

func dnsHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		wire, err := io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		response, err := makeDNSResponse(wire)
		if err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = io.Copy(w, bytes.NewReader(response))
	})
}

func makeDNSResponse(wire []byte) ([]byte, error) {
	var request dnsmessage.Message
	if err := request.Unpack(wire); err != nil {
		return nil, err
	}
	question := request.Questions[0]
	body := answerBody(question.Type)
	response := dnsmessage.Message{
		Header:    dnsmessage.Header{ID: request.ID, Response: true},
		Questions: request.Questions,
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: question.Name, Class: dnsmessage.ClassINET, TTL: 60},
			Body:   body,
		}},
	}
	return response.Pack()
}

func answerBody(qType dnsmessage.Type) dnsmessage.ResourceBody {
	switch qType {
	case dnsmessage.TypeA:
		return &dnsmessage.AResource{A: [4]byte{192, 0, 2, 10}}
	case dnsmessage.TypeAAAA:
		return &dnsmessage.AAAAResource{AAAA: [16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x10}}
	default:
		root, _ := dnsmessage.NewName(".")
		return &dnsmessage.HTTPSResource{SVCBResource: dnsmessage.SVCBResource{
			Priority: 1,
			Target:   root,
			Params:   []dnsmessage.SVCParam{{Key: dnsmessage.SVCParamECH, Value: testECHConfig}},
		}}
	}
}

func containsIP(ips []net.IP, want string) bool {
	for _, ip := range ips {
		if ip.String() == want {
			return true
		}
	}
	return false
}
