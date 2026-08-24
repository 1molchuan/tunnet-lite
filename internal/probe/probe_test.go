package probe

import (
	"context"
	"net"
	"testing"
	"time"
)

// listener returns a live loopback port plus a closer.
func listener(t *testing.T) (string, int) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	addr := l.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func TestRankTCPPutsReachableAddressesFirst(t *testing.T) {
	ip, port := listener(t)
	// 192.0.2.0/24 is TEST-NET-1 and is not routable, so it stands in for a
	// dead entry without depending on anything external.
	results := RankTCP(context.Background(),
		[]string{"192.0.2.1", ip, "192.0.2.2"}, port, 300*time.Millisecond)

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if !results[0].OK() || results[0].IP != ip {
		t.Fatalf("the reachable address should sort first, got %+v", results[0])
	}
	for _, r := range results[1:] {
		if r.OK() {
			t.Errorf("%s answered unexpectedly", r.IP)
		}
	}
}

func TestRankTCPKeepsUnreachableAddressesInTheTail(t *testing.T) {
	results := RankTCP(context.Background(),
		[]string{"192.0.2.1", "192.0.2.2"}, 443, 200*time.Millisecond)
	if len(results) != 2 {
		t.Fatalf("unreachable addresses must still be reported, got %d", len(results))
	}
	if got := Reachable(results); len(got) != 0 {
		t.Errorf("Reachable returned %v for an entirely dead pool", got)
	}
}

func TestReachableReturnsOnlyAnsweringAddresses(t *testing.T) {
	ip, port := listener(t)
	results := RankTCP(context.Background(),
		[]string{"192.0.2.1", ip}, port, 300*time.Millisecond)
	got := Reachable(results)
	if len(got) != 1 || got[0] != ip {
		t.Errorf("Reachable = %v, want [%s]", got, ip)
	}
}
