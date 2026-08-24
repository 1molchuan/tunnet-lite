// Package probe ranks CDN entry addresses by TCP round-trip time.
//
// This mirrors what the reference client does (a plain TCP RTT measurement to
// the entry address) rather than an end-to-end request through the tunnel. It
// is used to order the entry pool at startup and to choose the fallback entry;
// ongoing selection is left to Xray's own health checks, which measure the full
// path but cost a complete tunnel per probe.
package probe

import (
	"context"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Result struct {
	IP  string
	RTT time.Duration
	Err error
}

func (r Result) OK() bool { return r.Err == nil }

// RankTCP dials every address concurrently and returns them ordered by RTT,
// reachable first. Unreachable addresses are kept at the tail rather than
// dropped, so a pool that is entirely unreachable still yields candidates
// instead of an empty config.
func RankTCP(ctx context.Context, ips []string, port int, timeout time.Duration) []Result {
	results := make([]Result, len(ips))
	var wg sync.WaitGroup
	for i, ip := range ips {
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			results[i] = dial(ctx, ip, port, timeout)
		}(i, ip)
	}
	wg.Wait()

	sort.SliceStable(results, func(a, b int) bool {
		if results[a].OK() != results[b].OK() {
			return results[a].OK()
		}
		return results[a].RTT < results[b].RTT
	})
	return results
}

func dial(ctx context.Context, ip string, port int, timeout time.Duration) Result {
	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{IP: ip, Err: err}
	}
	rtt := time.Since(start)
	conn.Close()
	return Result{IP: ip, RTT: rtt}
}

// Reachable returns just the addresses that answered, best first.
func Reachable(results []Result) []string {
	var out []string
	for _, r := range results {
		if r.OK() {
			out = append(out, r.IP)
		}
	}
	return out
}
