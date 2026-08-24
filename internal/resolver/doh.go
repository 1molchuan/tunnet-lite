// Package resolver resolves names over DNS-over-HTTPS using IP-literal
// upstreams.
//
// The upstreams are addressed by IP so that resolving the resolver never needs
// DNS itself, and so the control-plane hostname never reaches the system
// resolver — which is what keeps it out of local DNS logs and away from the
// network's own recursor. The vendor client uses the same two upstreams.
package resolver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// These are the two IP-literal DoH endpoints the vendor client ships: AliDNS
// and DNSPod.
var defaultUpstreams = [...]string{
	"https://223.5.5.5/dns-query",
	"https://1.12.12.12/dns-query",
}

type DoH struct {
	Upstreams []string
	HTTP      *http.Client
}

func NewDoH(upstreams ...string) (*DoH, error) {
	validated, err := validateUpstreams(upstreams)
	if err != nil {
		return nil, err
	}
	return &DoH{
		Upstreams: validated,
		// A dedicated client: it must never be routed through anything that
		// would itself need a name resolved.
		HTTP: &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// LookupIP returns the A and AAAA addresses for name.
func (r *DoH) LookupIP(ctx context.Context, name string) ([]net.IP, error) {
	var ips []net.IP
	var queryErrs []error
	for _, qType := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA} {
		answers, err := r.query(ctx, name, qType)
		if err != nil {
			queryErrs = append(queryErrs, fmt.Errorf("%s: %w", qType, err))
			continue
		}
		for _, a := range answers {
			switch body := a.Body.(type) {
			case *dnsmessage.AResource:
				ips = append(ips, net.IP(body.A[:]))
			case *dnsmessage.AAAAResource:
				ips = append(ips, net.IP(body.AAAA[:]))
			}
		}
	}
	if len(ips) == 0 {
		if len(queryErrs) > 0 {
			return nil, fmt.Errorf("no address for %s over DoH: %w", name, errors.Join(queryErrs...))
		}
		return nil, fmt.Errorf("no address for %s over DoH", name)
	}
	return ips, nil
}

// LookupECH returns the ECHConfigList advertised in the name's HTTPS record, or
// nil when the record exists but carries no ech parameter.
func (r *DoH) LookupECH(ctx context.Context, name string) ([]byte, error) {
	answers, err := r.query(ctx, name, dnsmessage.TypeHTTPS)
	if err != nil {
		return nil, err
	}
	for _, a := range answers {
		var params []dnsmessage.SVCParam
		switch body := a.Body.(type) {
		case *dnsmessage.HTTPSResource:
			params = body.Params
		case *dnsmessage.SVCBResource:
			params = body.Params
		default:
			continue
		}
		for _, param := range params {
			if param.Key == dnsmessage.SVCParamECH {
				return slices.Clone(param.Value), nil
			}
		}
	}
	return nil, nil
}

func (r *DoH) query(ctx context.Context, name string, qType dnsmessage.Type) ([]dnsmessage.Resource, error) {
	wire, err := buildQuery(name, qType)
	if err != nil {
		return nil, err
	}

	var exchangeErrs []error
	for _, upstream := range r.Upstreams {
		answers, err := r.exchange(ctx, upstream, wire)
		if err == nil {
			return answers, nil
		}
		exchangeErrs = append(exchangeErrs, fmt.Errorf("%s: %w", upstream, err))
	}
	return nil, fmt.Errorf("DoH lookup of %s failed on every upstream: %w",
		name, errors.Join(exchangeErrs...))
}

func (r *DoH) exchange(ctx context.Context, upstream string, wire []byte) ([]dnsmessage.Resource, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(wire))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s returned %s", upstream, resp.Status)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/dns-message" {
		return nil, fmt.Errorf("%s returned invalid content type %q", upstream, resp.Header.Get("Content-Type"))
	}

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, err
	}
	return parseAnswers(payload)
}

func buildQuery(name string, qType dnsmessage.Type) ([]byte, error) {
	dnsName, err := dnsmessage.NewName(fqdn(name))
	if err != nil {
		return nil, err
	}
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{RecursionDesired: true},
		Questions: []dnsmessage.Question{{
			Name:  dnsName,
			Type:  qType,
			Class: dnsmessage.ClassINET,
		}},
	}
	return msg.Pack()
}

func parseAnswers(payload []byte) ([]dnsmessage.Resource, error) {
	var msg dnsmessage.Message
	if err := msg.Unpack(payload); err != nil {
		return nil, err
	}
	if !msg.Header.Response {
		return nil, errors.New("DNS payload is not a response")
	}
	if msg.Header.RCode != dnsmessage.RCodeSuccess {
		return nil, fmt.Errorf("DNS response code %v", msg.Header.RCode)
	}
	return msg.Answers, nil
}

func validateUpstreams(upstreams []string) ([]string, error) {
	selected := slices.Clone(upstreams)
	if len(selected) == 0 {
		selected = slices.Clone(defaultUpstreams[:])
	}
	for i, raw := range selected {
		selected[i] = strings.TrimSpace(raw)
		u, err := url.Parse(selected[i])
		if err != nil {
			return nil, fmt.Errorf("invalid DoH upstream %q: %w", raw, err)
		}
		if u.Scheme != "https" || u.User != nil || net.ParseIP(u.Hostname()) == nil {
			return nil, fmt.Errorf("DoH upstream %q must be an HTTPS URL with an IP-literal host", raw)
		}
	}
	return selected, nil
}

func fqdn(name string) string {
	if len(name) > 0 && name[len(name)-1] == '.' {
		return name
	}
	return name + "."
}
