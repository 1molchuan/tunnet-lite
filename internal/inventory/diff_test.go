package inventory

import (
	"strings"
	"testing"
)

func withHosts(hosts ...Host) *Inventory {
	inv := sample()
	inv.Hosts = hosts
	return inv
}

func TestCompareReportsNothingWhenNothingMoved(t *testing.T) {
	if c := Compare(sample(), sample()); !c.Empty() {
		t.Errorf("identical inventories reported %s", c)
	}
}

// The first poll has nothing to compare against, and announcing the whole
// inventory as new would be noise rather than news.
func TestCompareTreatsAMissingSideAsNoChange(t *testing.T) {
	if c := Compare(nil, sample()); !c.Empty() {
		t.Errorf("got %s", c)
	}
	if c := Compare(sample(), nil); !c.Empty() {
		t.Errorf("got %s", c)
	}
}

func TestCompareDetectsHostsComingAndGoing(t *testing.T) {
	key := sample().Hosts[0].Key
	prev := withHosts(Host{Slug: "a", Key: key, Online: true}, Host{Slug: "b", Key: key, Online: true})
	next := withHosts(Host{Slug: "a", Key: key, Online: true}, Host{Slug: "c", Key: key, Online: true})

	c := Compare(prev, next)
	if len(c.HostsAdded) != 1 || c.HostsAdded[0] != "c" {
		t.Errorf("added = %v", c.HostsAdded)
	}
	if len(c.HostsRemoved) != 1 || c.HostsRemoved[0] != "b" {
		t.Errorf("removed = %v", c.HostsRemoved)
	}
}

func TestCompareDetectsOnlineChangesAndKeyRotation(t *testing.T) {
	key := sample().Hosts[0].Key
	other := sample().Hosts[1].Key
	prev := withHosts(
		Host{Slug: "a", Key: key, Online: true},
		Host{Slug: "b", Key: key, Online: false},
		Host{Slug: "c", Key: key, Online: true})
	next := withHosts(
		Host{Slug: "a", Key: key, Online: false},
		Host{Slug: "b", Key: key, Online: true},
		Host{Slug: "c", Key: other + "x", Online: true})

	c := Compare(prev, next)
	if len(c.WentOffline) != 1 || c.WentOffline[0] != "a" {
		t.Errorf("offline = %v", c.WentOffline)
	}
	if len(c.WentOnline) != 1 || c.WentOnline[0] != "b" {
		t.Errorf("online = %v", c.WentOnline)
	}
	if len(c.KeysRotated) != 1 || c.KeysRotated[0] != "c" {
		t.Errorf("rotated = %v", c.KeysRotated)
	}
}

// The control plane makes no promise about ordering, so a reshuffled pool is
// not something to wake anyone up about.
func TestCompareIgnoresPoolReordering(t *testing.T) {
	prev := sample()
	next := sample()
	next.EntryGroups[0].IPv4 = []string{"192.0.2.9", "192.0.2.1"}
	prev.EntryGroups[0].IPv4 = []string{"192.0.2.1", "192.0.2.9"}

	if c := Compare(prev, next); len(c.PoolsChanged) != 0 {
		t.Errorf("reordering was reported as a change: %s", c)
	}

	next.EntryGroups[0].IPv4 = []string{"192.0.2.1", "198.51.100.7"}
	c := Compare(prev, next)
	if len(c.PoolsChanged) != 1 {
		t.Errorf("a real pool change was missed: %s", c)
	}
}

func TestCompareDetectsRootDomainRotation(t *testing.T) {
	prev := sample()
	next := sample()
	next.RootDomains = []string{"a.example", "c.example"}

	c := Compare(prev, next)
	if len(c.RootsAdded) != 1 || c.RootsAdded[0] != "c.example" {
		t.Errorf("added = %v", c.RootsAdded)
	}
	if len(c.RootsRemoved) != 1 || c.RootsRemoved[0] != "b.example" {
		t.Errorf("removed = %v", c.RootsRemoved)
	}
}

func TestStringOmitsUnchangedKinds(t *testing.T) {
	c := Changes{HostsRemoved: []string{"tyo-01"}}
	got := c.String()
	if !strings.Contains(got, "exits removed: tyo-01") {
		t.Errorf("got %q", got)
	}
	if strings.Contains(got, "added") || strings.Contains(got, "rotated") {
		t.Errorf("unchanged kinds leaked into %q", got)
	}
}
