// Command tunnet-lite runs a local SOCKS proxy over the TunNet data plane.
//
// It reads a node inventory (from disk, or fetched from the control plane),
// ranks the operator's CDN entry pool by TCP round-trip time, renders an Xray
// configuration and serves it in-process. No credentials or node addresses are
// compiled in.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1molchuan/tunnet-lite/internal/console"
	"github.com/1molchuan/tunnet-lite/internal/control"
	"github.com/1molchuan/tunnet-lite/internal/engine"
	"github.com/1molchuan/tunnet-lite/internal/inventory"
	"github.com/1molchuan/tunnet-lite/internal/supervisor"
	"github.com/1molchuan/tunnet-lite/internal/xcfg"
)

func main() {
	log.SetFlags(log.Ltime)

	var (
		nodesPath = flag.String("nodes", "nodes.json", "node inventory file")
		listen    = flag.String("listen", "127.0.0.1", "SOCKS bind address")
		port      = flag.Int("port", 18080, "SOCKS port")
		udp       = flag.Bool("udp", false, "enable SOCKS UDP associate (see README: the nodes reject mux, so UDP does not traverse)")

		host  = flag.String("host", "", "exit slug (default: first online host)")
		group = flag.String("entry-group", "", "operator ingress name or substring (default: first)")
		root  = flag.String("root", "", "pin a root domain (default: cached, else random)")

		noFront    = flag.Bool("no-front", false, "dial the CDN directly, skipping the front proxy")
		maxEntries = flag.Int("max-entries", 0, "cap how many entry addresses enter the pool (0 = all reachable)")
		probeTO    = flag.Duration("probe-timeout", 3*time.Second, "TCP probe timeout for entry ranking")

		probeURL       = flag.String("health-url", "", "health check URL (default: gstatic generate_204)")
		healthInterval = flag.Duration("health-interval", 60*time.Second, "health check interval; must exceed -health-timeout")
		healthTimeout  = flag.Duration("health-timeout", 15*time.Second, "health check timeout; each probe builds a full tunnel")

		mode    = flag.String("enc-mode", xcfg.DefaultMode, "VLESS Encryption mode: native, xorpub or random")
		rtt     = flag.String("enc-rtt", xcfg.DefaultRTT, "VLESS Encryption handshake: 1rtt or 0rtt")
		padding = flag.String("enc-padding", xcfg.DefaultPadding, "VLESS Encryption padding spec")
		flow    = flag.String("flow", xcfg.DefaultFlow, "VLESS flow; use \"none\" to disable it")

		identity    = flag.String("identity", "tunnet-lite-identity.json", "path for the persisted control-plane identity")
		refresh     = flag.Bool("refresh", false, "fetch the node inventory from the control plane, then exit")
		autoApprove = flag.Bool("auto-approve", false, "approve a pending authorisation without opening the verification page")
		verifyURL   = flag.String("verify-url", "", "override the verification page URL")
		controlURL  = flag.String("control-url", control.DefaultEndpoint, "control-plane endpoint")

		interactive = flag.Bool("console", false, "drop into the interactive console instead of just serving")
		statePath   = flag.String("state", "tunnet-lite-state.json", "path for persisted choices")
		logLevel    = flag.String("log-level", "warning", "xray log level")
		dump        = flag.Bool("dump-config", false, "print the rendered config and exit")
	)
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *refresh {
		if err := refreshInventory(ctx, *identity, *nodesPath, *controlURL, *autoApprove, *verifyURL); err != nil {
			log.Fatalf("%v", err)
		}
		return
	}

	opts := supervisor.Options{
		Listen: *listen, Port: *port, UDP: *udp,
		HostSlug: *host, EntryGroup: *group, RootDomain: *root,
		NoFront: *noFront, MaxEntries: *maxEntries, ProbeTimeout: *probeTO,
		ProbeURL: *probeURL, HealthInterval: *healthInterval, HealthTimeout: *healthTimeout,
		Mode: *mode, RTT: *rtt, Padding: *padding, Flow: *flow,
		StatePath: *statePath, LogLevel: *logLevel,
	}

	inv, err := inventory.Load(*nodesPath)
	if err != nil {
		log.Fatalf("%v", err)
	}

	if *dump {
		_, configJSON, err := supervisor.Resolve(ctx, inv, opts)
		if err != nil {
			log.Fatalf("%v", err)
		}
		fmt.Println(string(configJSON))
		return
	}

	if err := run(ctx, inv, opts, *interactive, *identity, *nodesPath); err != nil {
		log.Fatalf("%v", err)
	}
}

func run(ctx context.Context, inv *inventory.Inventory, opts supervisor.Options,
	interactive bool, identityPath, nodesPath string) error {
	eng := engine.New()
	defer eng.Stop()

	if _, err := supervisor.Start(ctx, eng, inv, opts); err != nil {
		return err
	}

	if !interactive {
		<-ctx.Done()
		log.Printf("shutting down")
		return nil
	}

	// The console can refresh the inventory only if an identity already exists;
	// creating one stays an explicit step rather than a side effect.
	var session *control.Session
	if s, fresh, err := control.Open(identityPath, nodesPath); err == nil && !fresh {
		session = s
	}

	err := console.New(eng, inv, opts, session, os.Stdin, os.Stdout).Run(ctx)
	log.Printf("shutting down")
	return err
}

// refreshInventory pulls a fresh directory from the control plane. A brand new
// identity has to be approved once; after that the same identity refreshes
// silently with a sync.
func refreshInventory(ctx context.Context, identityPath, nodesPath, controlURL string,
	autoApprove bool, verifyURL string) error {
	session, fresh, err := control.Open(identityPath, nodesPath)
	if err != nil {
		return err
	}
	session.Client.Endpoint = controlURL
	if fresh {
		log.Printf("created a new client identity at %s", identityPath)
	}

	inv, err := session.Refresh(ctx, control.RefreshOptions{
		AutoApprove: autoApprove, VerificationURL: verifyURL,
	})
	var needsAuth *control.NeedsAuthorizationError
	if errors.As(err, &needsAuth) {
		log.Printf("approve this client at: %s", needsAuth.VerificationURL)
		log.Printf("then run again with -refresh to finish")
		return err
	}
	if err != nil {
		return err
	}

	log.Printf("wrote %s: %d hosts, %d entry groups, %d root domains",
		nodesPath, len(inv.Hosts), len(inv.EntryGroups), len(inv.RootDomains))
	return nil
}
