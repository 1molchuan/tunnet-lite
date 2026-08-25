package inventory

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Changes describes how one inventory differs from another. It exists so a
// refresh can report what actually moved rather than announcing every poll.
type Changes struct {
	HostsAdded   []string
	HostsRemoved []string
	WentOnline   []string
	WentOffline  []string
	KeysRotated  []string

	GroupsAdded   []string
	GroupsRemoved []string
	PoolsChanged  []string

	RootsAdded   []string
	RootsRemoved []string
}

func (c Changes) Empty() bool {
	return len(c.HostsAdded) == 0 && len(c.HostsRemoved) == 0 &&
		len(c.WentOnline) == 0 && len(c.WentOffline) == 0 &&
		len(c.KeysRotated) == 0 && len(c.GroupsAdded) == 0 &&
		len(c.GroupsRemoved) == 0 && len(c.PoolsChanged) == 0 &&
		len(c.RootsAdded) == 0 && len(c.RootsRemoved) == 0
}

// String renders the changes as one line per kind, omitting the kinds that did
// not change.
func (c Changes) String() string {
	var lines []string
	add := func(label string, values []string) {
		if len(values) > 0 {
			lines = append(lines, fmt.Sprintf("%s: %s", label, strings.Join(values, ", ")))
		}
	}
	add("exits added", c.HostsAdded)
	add("exits removed", c.HostsRemoved)
	add("came online", c.WentOnline)
	add("went offline", c.WentOffline)
	add("keys rotated", c.KeysRotated)
	add("ingresses added", c.GroupsAdded)
	add("ingresses removed", c.GroupsRemoved)
	add("address pools changed", c.PoolsChanged)
	add("root domains added", c.RootsAdded)
	add("root domains removed", c.RootsRemoved)
	return strings.Join(lines, "; ")
}

// Compare reports what changed going from prev to next. A nil prev means
// everything is new, which is reported as no change: there is nothing for the
// operator to act on the first time round.
func Compare(prev, next *Inventory) Changes {
	var c Changes
	if prev == nil || next == nil {
		return c
	}

	prevHosts := hostsBySlug(prev)
	nextHosts := hostsBySlug(next)
	c.HostsAdded = sortedMissing(nextHosts, prevHosts)
	c.HostsRemoved = sortedMissing(prevHosts, nextHosts)
	for _, slug := range slices.Sorted(maps.Keys(nextHosts)) {
		before, ok := prevHosts[slug]
		if !ok {
			continue
		}
		after := nextHosts[slug]
		switch {
		case !before.Online && after.Online:
			c.WentOnline = append(c.WentOnline, slug)
		case before.Online && !after.Online:
			c.WentOffline = append(c.WentOffline, slug)
		}
		if before.Key != after.Key {
			c.KeysRotated = append(c.KeysRotated, slug)
		}
	}

	prevGroups := groupsByName(prev)
	nextGroups := groupsByName(next)
	c.GroupsAdded = sortedMissing(nextGroups, prevGroups)
	c.GroupsRemoved = sortedMissing(prevGroups, nextGroups)
	for _, name := range slices.Sorted(maps.Keys(nextGroups)) {
		before, ok := prevGroups[name]
		if !ok {
			continue
		}
		if !sameAddresses(before.IPv4, nextGroups[name].IPv4) {
			c.PoolsChanged = append(c.PoolsChanged, name)
		}
	}

	c.RootsAdded = sortedDifference(next.RootDomains, prev.RootDomains)
	c.RootsRemoved = sortedDifference(prev.RootDomains, next.RootDomains)
	return c
}

func hostsBySlug(inv *Inventory) map[string]Host {
	out := make(map[string]Host, len(inv.Hosts))
	for _, h := range inv.Hosts {
		out[h.Slug] = h
	}
	return out
}

func groupsByName(inv *Inventory) map[string]EntryGroup {
	out := make(map[string]EntryGroup, len(inv.EntryGroups))
	for _, g := range inv.EntryGroups {
		out[g.Name] = g
	}
	return out
}

func sortedMissing[T any](have, from map[string]T) []string {
	var out []string
	for key := range have {
		if _, ok := from[key]; !ok {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

func sortedDifference(have, from []string) []string {
	var out []string
	for _, v := range have {
		if !slices.Contains(from, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// sameAddresses ignores ordering: the control plane does not promise a stable
// order, and a reshuffle is not a change worth reporting.
func sameAddresses(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
