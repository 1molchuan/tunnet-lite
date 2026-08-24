package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// authorizePath is the endpoint the verification page itself posts to. The page
// is served from a rotating root domain, so the origin is taken from whatever
// URL the bootstrap response supplied rather than being fixed here.
const authorizePath = "/api/v1/access/authorize"

var urlPattern = regexp.MustCompile(`https://[A-Za-z0-9.\-]+(?:/[^\s"']*)?`)

// VerificationURL digs the human-facing verification link out of a bootstrap
// payload. The field name has moved between versions, so rather than binding to
// one key the whole payload is scanned for an https URL whose host starts with
// "access.".
func VerificationURL(payload []byte) string {
	for _, candidate := range urlPattern.FindAllString(string(payload), -1) {
		u, err := url.Parse(strings.TrimRight(candidate, `",`))
		if err != nil {
			continue
		}
		if strings.HasPrefix(u.Host, "access.") {
			return u.String()
		}
	}
	return ""
}

// Authorize approves a pending ticket by calling the same endpoint the
// verification page calls. verificationURL supplies the origin; pass the URL
// from the bootstrap response, or an explicit override.
//
// This is the step a human normally performs in a browser. Calling it directly
// only makes sense for your own client on your own account.
func (c *Client) Authorize(ctx context.Context, verificationURL, ticket string) error {
	origin, err := originOf(verificationURL)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"ticket": ticket})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+authorizePath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("authorize: %w", err)
	}
	defer resp.Body.Close()
	payload := make([]byte, 2048)
	n, _ := resp.Body.Read(payload)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authorize rejected (%s): %s", resp.Status, truncate(payload[:n], 512))
	}
	return nil
}

func originOf(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("no verification URL available; pass one explicitly")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("bad verification URL %q: %w", raw, err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("bad verification URL %q", raw)
	}
	return "https://" + u.Host, nil
}
