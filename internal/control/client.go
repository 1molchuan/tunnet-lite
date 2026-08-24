// Package control speaks the TunNet control plane: it signs requests with
// RFC 9421 HTTP Message Signatures and decrypts the HPKE-sealed responses that
// carry the node directory.
//
// Every response is encrypted to a per-request ephemeral X25519 key, so the
// directory is not readable by anything that merely terminates TLS in between.
package control

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	DefaultEndpoint = "https://client-api.nexttun.net/api/v1/client"
	mediaType       = "application/vnd.tunnet.hpke"
	hpkeInfo        = "TunNet/client-response/v1"
	signatureTag    = "tunnet-client-v1"
	defaultVersion  = "0.2.4"
	defaultPlatform = "windows"
)

// Client performs signed, encrypted control-plane calls.
type Client struct {
	Endpoint   string
	Platform   string
	AppVersion string
	HTTP       *http.Client
	Identity   *Identity
	ExpectECH  bool

	reportTLS sync.Once
}

func NewClient(id *Identity) *Client {
	return &Client{
		Endpoint:   DefaultEndpoint,
		Platform:   defaultPlatform,
		AppVersion: defaultVersion,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
		Identity:   id,
	}
}

// exchange holds the per-request material: the nonce that binds the signature
// and the ephemeral key the response is sealed to.
type exchange struct {
	operation   string
	nonce       string
	nonceRaw    []byte
	responseKey hpke.PrivateKey
	responsePub string
}

func newExchange(operation string) (*exchange, error) {
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		return nil, err
	}
	key, err := hpke.DHKEM(ecdh.X25519()).GenerateKey()
	if err != nil {
		return nil, err
	}
	return &exchange{
		operation:   operation,
		nonce:       base64.RawURLEncoding.EncodeToString(nonceRaw),
		nonceRaw:    nonceRaw,
		responseKey: key,
		responsePub: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
	}, nil
}

type identityRequest struct {
	ClientID   string `json:"client_id"`
	Platform   string `json:"platform"`
	AppVersion string `json:"app_version"`
}

type ticketRequest struct {
	Ticket     string `json:"ticket"`
	AppVersion string `json:"app_version"`
}

// BootstrapResult reports what the control plane wants next. When Ticket is set
// the identity is not authorised yet and the ticket must be approved before
// Access will return a directory.
type BootstrapResult struct {
	State      string
	Ticket     string
	RetryAfter time.Duration
	Raw        []byte
}

func (c *Client) Bootstrap(ctx context.Context) (*BootstrapResult, error) {
	body, err := json.Marshal(identityRequest{c.Identity.ClientID, c.Platform, c.AppVersion})
	if err != nil {
		return nil, err
	}
	plain, err := c.call(ctx, "bootstrap", body)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Access struct {
			State             string `json:"state"`
			Ticket            string `json:"ticket"`
			RetryAfterSeconds int    `json:"retry_after_seconds"`
		} `json:"access"`
	}
	if err := json.Unmarshal(plain, &parsed); err != nil {
		return nil, fmt.Errorf("parse bootstrap response: %w", err)
	}
	return &BootstrapResult{
		State:      parsed.Access.State,
		Ticket:     parsed.Access.Ticket,
		RetryAfter: time.Duration(parsed.Access.RetryAfterSeconds) * time.Second,
		Raw:        plain,
	}, nil
}

// Access exchanges an approved ticket for the full runtime directory.
func (c *Client) Access(ctx context.Context, ticket string) ([]byte, error) {
	body, err := json.Marshal(ticketRequest{ticket, c.AppVersion})
	if err != nil {
		return nil, err
	}
	return c.call(ctx, "access", body)
}

// Sync refreshes the directory for an already authorised identity.
func (c *Client) Sync(ctx context.Context) ([]byte, error) {
	body, err := json.Marshal(identityRequest{c.Identity.ClientID, c.Platform, c.AppVersion})
	if err != nil {
		return nil, err
	}
	return c.call(ctx, "sync", body)
}

func (c *Client) call(ctx context.Context, operation string, body []byte) ([]byte, error) {
	ex, err := newExchange(operation)
	if err != nil {
		return nil, err
	}
	req, err := c.signedRequest(ctx, ex, body, time.Now())
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	defer resp.Body.Close()

	c.reportTLSOnce(resp)

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), mediaType) {
		// A plaintext body means the call was rejected; surface it verbatim
		// but bounded, since it is server-controlled text.
		return nil, fmt.Errorf("%s rejected (%s): %s",
			operation, resp.Status, truncate(payload, 512))
	}
	plain, err := c.open(ex, payload)
	if err != nil {
		return nil, fmt.Errorf("%s: decrypt response: %w", operation, err)
	}
	return plain, nil
}

// reportTLSOnce states, once per client, whether the protections that were
// configured actually took effect. Configuring ECH and having the server accept
// it are different things, and a silent downgrade is exactly what this is meant
// to make visible.
func (c *Client) reportTLSOnce(resp *http.Response) {
	c.reportTLS.Do(func() {
		if resp.TLS == nil {
			return
		}
		if !c.ExpectECH {
			return
		}
		if resp.TLS.ECHAccepted {
			log.Printf("control TLS: ECH accepted, hostname hidden from the handshake")
			return
		}
		log.Printf("control TLS: ECH not in effect, the hostname is visible in the handshake")
	})
}

func (c *Client) signedRequest(ctx context.Context, ex *exchange, body []byte, now time.Time) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", mediaType+", application/json")
	req.Header.Set("Tunnet-Public-Key", c.Identity.PublicKey())
	req.Header.Set("Tunnet-Response-Key", ex.responsePub)
	req.Header.Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(digest[:])+":")

	created := now.Unix()
	params := signatureParams(c.Identity.ClientID, ex, created)
	sig := ed25519.Sign(c.Identity.SigningKey, []byte(signatureBase(req, params)))
	req.Header.Set("Signature-Input", "tn="+params)
	req.Header.Set("Signature", "tn=:"+base64.StdEncoding.EncodeToString(sig)+":")
	return req, nil
}

const signedComponents = `("@method" "@authority" "@path" "content-type" "content-digest" "tunnet-public-key" "tunnet-response-key")`

func signatureParams(clientID string, ex *exchange, created int64) string {
	return fmt.Sprintf(`%s;created=%d;expires=%d;keyid="%s";alg="ed25519";nonce="%s";tag="%s:%s"`,
		signedComponents, created, created+120, clientID, ex.nonce, ex.operation, signatureTag)
}

func signatureBase(req *http.Request, params string) string {
	return strings.Join([]string{
		`"@method": ` + req.Method,
		`"@authority": ` + req.URL.Host,
		`"@path": ` + req.URL.EscapedPath(),
		`"content-type": ` + req.Header.Get("Content-Type"),
		`"content-digest": ` + req.Header.Get("Content-Digest"),
		`"tunnet-public-key": ` + req.Header.Get("Tunnet-Public-Key"),
		`"tunnet-response-key": ` + req.Header.Get("Tunnet-Response-Key"),
		`"@signature-params": ` + params,
	}, "\n")
}

// open unseals a response. The info string binds the ciphertext to this
// operation, this client and this request's nonce, so a response cannot be
// replayed into a different call.
func (c *Client) open(ex *exchange, ciphertext []byte) ([]byte, error) {
	info := []byte(hpkeInfo)
	info = append(info, 0)
	info = append(info, ex.operation...)
	info = append(info, 0)
	info = append(info, c.Identity.ClientID...)
	info = append(info, 0)
	info = append(info, ex.nonceRaw...)
	return hpke.Open(ex.responseKey, hpke.HKDFSHA256(), hpke.AES256GCM(), info, ciphertext)
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
