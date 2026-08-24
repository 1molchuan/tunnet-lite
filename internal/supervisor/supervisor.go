// Package supervisor turns an inventory plus a set of choices into a runnable
// configuration, and drives the engine that serves it.
package supervisor

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/1molchuan/tunnet-lite/internal/engine"
	"github.com/1molchuan/tunnet-lite/internal/inventory"
	"github.com/1molchuan/tunnet-lite/internal/probe"
	"github.com/1molchuan/tunnet-lite/internal/xcfg"
)

type Options struct {
	Listen string
	Port   int

	HostSlug   string // exit slug; empty picks the first online host
	EntryGroup string // operator ingress; empty picks the first group
	RootDomain string // pin a root domain; empty uses the cached or a random one
	NoFront    bool   // dial the CDN directly instead of via the front proxy
	UDP        bool   // enable SOCKS UDP associate

	MaxEntries   int
	ProbeTimeout time.Duration

	ProbeURL       string
	HealthInterval time.Duration
	HealthTimeout  time.Duration

	Mode, RTT, Padding, Flow string

	StatePath string
	LogLevel  string
}

// Resolve makes every choice the configuration depends on and renders it.
// It is separate from starting the engine so a caller can inspect the plan, or
// render a config without serving it.
func Resolve(ctx context.Context, inv *inventory.Inventory, o Options) (engine.Plan, []byte, error) {
	var plan engine.Plan

	host, err := inv.SelectHost(o.HostSlug)
	if err != nil {
		return plan, nil, err
	}
	key, err := host.NormalizedKey()
	if err != nil {
		return plan, nil, err
	}
	group, err := inv.SelectGroup(o.EntryGroup)
	if err != nil {
		return plan, nil, err
	}

	st := loadState(o.StatePath)
	root, err := inv.SelectRoot(o.RootDomain, st.RootDomain)
	if err != nil {
		return plan, nil, err
	}
	if root != st.RootDomain {
		st.RootDomain = root
		if err := saveState(o.StatePath, st); err != nil {
			log.Printf("warning: could not persist state to %s: %v", o.StatePath, err)
		}
	}

	entries, err := rankEntries(ctx, group, o)
	if err != nil {
		return plan, nil, err
	}

	front := group.FrontProxy
	if o.NoFront {
		front = nil
	} else if front == nil {
		return plan, nil, fmt.Errorf(
			"entry group %q has no front proxy; use the direct option to dial the CDN instead", group.Name)
	}

	if o.UDP {
		// Vision routes every UDP association through Xray's mux, and this
		// operator's nodes do not answer mux at all: a plain TCP request with
		// mux enabled fails the same way. So the listener will accept UDP
		// locally but nothing will come back. Left switchable so the
		// limitation can be re-tested, but never silently.
		log.Printf("warning: UDP is unlikely to work — this operator's nodes reject Xray mux, " +
			"which Vision requires for UDP. Use socks5h so names resolve over TCP.")
	}

	plan = engine.Plan{
		HostSlug: host.Slug, HostName: host.Name,
		LogicalHost: host.Slug + "." + root, RootDomain: root,
		GroupName: group.Name, Entries: entries,
		UDP: o.UDP, Listen: o.Listen, Port: o.Port,
	}
	if front != nil {
		plan.FrontProxy = front.Endpoint
	}

	configJSON, err := xcfg.Build(xcfg.Options{
		Listen: o.Listen, Port: o.Port,
		ClientID: inv.ClientID, LogicalHost: plan.LogicalHost, EncryptionKey: key,
		Entries: entries, FrontProxy: front, UDP: o.UDP,
		Mode: o.Mode, RTT: o.RTT, Padding: o.Padding, Flow: o.Flow,
		ProbeURL: o.ProbeURL, ProbeInterval: o.HealthInterval, ProbeTimeout: o.HealthTimeout,
		LogLevel: o.LogLevel,
	})
	if err != nil {
		return plan, nil, err
	}
	return plan, configJSON, nil
}

// Start resolves and applies a configuration to the engine.
func Start(ctx context.Context, eng *engine.Engine, inv *inventory.Inventory, o Options) (engine.Plan, error) {
	plan, configJSON, err := Resolve(ctx, inv, o)
	if err != nil {
		return plan, err
	}
	if err := eng.Apply(plan, configJSON); err != nil {
		return plan, err
	}
	logPlan(plan)
	return plan, nil
}

func logPlan(p engine.Plan) {
	log.Printf("exit  %s (%s) via %s", p.HostSlug, p.HostName, p.LogicalHost)
	if p.FrontProxy != "" {
		log.Printf("entry %s via %s, %d addresses", p.GroupName, p.FrontProxy, len(p.Entries))
	} else {
		log.Printf("entry %s direct, %d addresses", p.GroupName, len(p.Entries))
	}
	udp := "tcp only"
	if p.UDP {
		udp = "tcp+udp"
	}
	log.Printf("listening on socks5://%s:%d (%s)", p.Listen, p.Port, udp)
}

// rankEntries orders the pool by TCP RTT so the balancer's fallback is the
// fastest reachable address from the start, before any health data exists.
func rankEntries(ctx context.Context, group inventory.EntryGroup, o Options) ([]string, error) {
	timeout := o.ProbeTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	results := probe.RankTCP(ctx, group.IPv4, 443, timeout)
	entries := probe.Reachable(results)
	if len(entries) == 0 {
		return nil, fmt.Errorf("no reachable address in entry group %q", group.Name)
	}
	for _, r := range results {
		if r.OK() {
			log.Printf("probe %-16s %v", r.IP, r.RTT.Round(time.Millisecond))
		} else {
			log.Printf("probe %-16s unreachable", r.IP)
		}
	}
	if o.MaxEntries > 0 && len(entries) > o.MaxEntries {
		entries = entries[:o.MaxEntries]
	}
	return entries, nil
}
