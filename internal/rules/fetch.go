// Package rules fetches the routing rule sets smart mode needs.
//
// They are not embedded, so that which rules are in force stays visible and can
// be updated independently. That leaves the client responsible for retrieving
// them, and a truncated download is the failure worth guarding against: the
// file looks intact and only surfaces much later as an unhelpful "EOF" from the
// geo data loader. Every file is therefore checked against its published digest
// and only moved into place once it matches.
package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL publishes both files alongside a .sha256sum for each.
const DefaultBaseURL = "https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download"

// Files are the rule sets Xray resolves as geoip: and geosite: references.
var Files = []string{"geoip.dat", "geosite.dat"}

type Fetcher struct {
	BaseURL string
	HTTP    *http.Client
	// Attempts bounds retries of a file whose digest did not match. A mismatch
	// is usually a truncated transfer, which a retry fixes.
	Attempts int
}

func NewFetcher() *Fetcher {
	return &Fetcher{
		BaseURL:  DefaultBaseURL,
		HTTP:     &http.Client{Timeout: 15 * time.Minute},
		Attempts: 3,
	}
}

// Fetch downloads every rule set into dir, reporting progress through log.
func (f *Fetcher) Fetch(ctx context.Context, dir string, log func(string, ...any)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range Files {
		if err := f.fetchOne(ctx, dir, name, log); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (f *Fetcher) fetchOne(ctx context.Context, dir, name string, log func(string, ...any)) error {
	want, err := f.digest(ctx, name)
	if err != nil {
		return err
	}

	target := filepath.Join(dir, name)
	if got, err := fileDigest(target); err == nil && got == want {
		log("%s is already current", name)
		return nil
	}

	attempts := max(f.Attempts, 1)
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		size, got, err := f.download(ctx, name, target+".part")
		if err != nil {
			lastErr = err
			continue
		}
		if got != want {
			os.Remove(target + ".part")
			lastErr = fmt.Errorf("digest mismatch (attempt %d of %d); the download was probably truncated",
				attempt, attempts)
			continue
		}
		if err := os.Rename(target+".part", target); err != nil {
			return err
		}
		log("%s ok (%.1f MB)", name, float64(size)/(1<<20))
		return nil
	}
	return lastErr
}

// digest reads the published checksum, which is "<hex>  <filename>".
func (f *Fetcher) digest(ctx context.Context, name string) (string, error) {
	body, err := f.get(ctx, name+".sha256sum")
	if err != nil {
		return "", err
	}
	defer body.Close()

	data, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", fmt.Errorf("unusable checksum file: %q", strings.TrimSpace(string(data)))
	}
	return strings.ToLower(fields[0]), nil
}

func (f *Fetcher) download(ctx context.Context, name, dest string) (int64, string, error) {
	body, err := f.get(ctx, name)
	if err != nil {
		return 0, "", err
	}
	defer body.Close()

	out, err := os.Create(dest)
	if err != nil {
		return 0, "", err
	}
	defer out.Close()

	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, sum), body)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(sum.Sum(nil)), nil
}

func (f *Fetcher) get(ctx context.Context, name string) (io.ReadCloser, error) {
	url := strings.TrimSuffix(f.BaseURL, "/") + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s returned %s", url, resp.Status)
	}
	return resp.Body, nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
