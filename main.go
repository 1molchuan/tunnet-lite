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
	"strings"
	"syscall"
	"time"

	"github.com/1molchuan/tunnet-lite/internal/console"
	"github.com/1molchuan/tunnet-lite/internal/control"
	"github.com/1molchuan/tunnet-lite/internal/engine"
	"github.com/1molchuan/tunnet-lite/internal/inventory"
	"github.com/1molchuan/tunnet-lite/internal/pinning"
	"github.com/1molchuan/tunnet-lite/internal/resolver"
	"github.com/1molchuan/tunnet-lite/internal/supervisor"
	"github.com/1molchuan/tunnet-lite/internal/xcfg"
)

func main() {
	log.SetFlags(log.Ltime)
	options := parseCLI()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, options); err != nil {
		log.Fatalf("%v", err)
	}
}

type cliOptions struct {
	nodesPath   string
	identity    string
	controlURL  string
	dohList     string
	pinMode     string
	pinsPath    string
	verifyURL   string
	routeMode   string
	useECH      bool
	refresh     bool
	autoApprove bool
	interactive bool
	dump        bool
	supervisor  supervisor.Options
}

func parseCLI() cliOptions {
	var options cliOptions
	registerProxyFlags(&options)
	registerWireFlags(&options)
	registerControlFlags(&options)
	flag.Parse()
	return options
}

func registerProxyFlags(options *cliOptions) {
	flag.StringVar(&options.nodesPath, "nodes", "nodes.json", "node inventory file")
	flag.StringVar(&options.supervisor.Listen, "listen", "127.0.0.1", "SOCKS bind address")
	flag.IntVar(&options.supervisor.Port, "port", 18080, "SOCKS port")
	flag.BoolVar(&options.supervisor.UDP, "udp", false, "enable SOCKS UDP associate (nodes reject mux)")
	flag.StringVar(&options.supervisor.HostSlug, "host", "", "exit slug (default: first online host)")
	flag.StringVar(&options.supervisor.EntryGroup, "entry-group", "", "operator ingress name or substring")
	flag.StringVar(&options.supervisor.RootDomain, "root", "", "pin a root domain")
	flag.BoolVar(&options.supervisor.NoFront, "no-front", false, "dial the CDN directly")
	flag.StringVar(&options.routeMode, "route", string(xcfg.RouteGlobal),
		"how much traffic the tunnel carries: global or smart")
	flag.StringVar(&options.supervisor.Assets, "assets", "",
		"directory holding geoip.dat and geosite.dat (default: next to the binary)")
	flag.StringVar(&options.supervisor.DomainStrategy, "route-domain-strategy", "",
		"router domain strategy: AsIs, IPIfNonMatch or IPOnDemand")
	flag.IntVar(&options.supervisor.MaxEntries, "max-entries", 0, "cap entry addresses (0 = all reachable)")
	flag.DurationVar(&options.supervisor.ProbeTimeout, "probe-timeout", 3*time.Second, "entry TCP probe timeout")
}

func registerWireFlags(options *cliOptions) {
	flag.StringVar(&options.supervisor.ProbeURL, "health-url", "", "health check URL")
	flag.DurationVar(&options.supervisor.HealthInterval, "health-interval", 60*time.Second, "health check interval")
	flag.DurationVar(&options.supervisor.HealthTimeout, "health-timeout", 15*time.Second, "health check timeout")
	flag.StringVar(&options.supervisor.Mode, "enc-mode", xcfg.DefaultMode, "VLESS Encryption mode")
	flag.StringVar(&options.supervisor.RTT, "enc-rtt", xcfg.DefaultRTT, "VLESS Encryption handshake")
	flag.StringVar(&options.supervisor.Padding, "enc-padding", xcfg.DefaultPadding, "VLESS Encryption padding")
	flag.StringVar(&options.supervisor.Flow, "flow", xcfg.DefaultFlow, "VLESS flow; use \"none\" to disable")
	flag.StringVar(&options.supervisor.StatePath, "state", "tunnet-lite-state.json", "persisted choices")
	flag.StringVar(&options.supervisor.LogLevel, "log-level", "warning", "xray log level")
	flag.BoolVar(&options.interactive, "console", false, "open the interactive console")
	flag.BoolVar(&options.dump, "dump-config", false, "print the rendered config and exit")
}

func registerControlFlags(options *cliOptions) {
	flag.StringVar(&options.identity, "identity", "tunnet-lite-identity.json", "control-plane identity path")
	flag.BoolVar(&options.refresh, "refresh", false, "fetch the node inventory, then exit")
	flag.BoolVar(&options.autoApprove, "auto-approve", false, "approve a pending authorisation")
	flag.StringVar(&options.verifyURL, "verify-url", "", "override the verification page URL")
	flag.StringVar(&options.controlURL, "control-url", control.DefaultEndpoint, "control-plane endpoint")
	flag.StringVar(&options.dohList, "doh", "", "IP-literal DoH URLs; \"off\" uses system DNS")
	flag.BoolVar(&options.useECH, "ech", true, "require ECH on the control connection")
	flag.StringVar(&options.pinMode, "pin-mode", "tofu", "certificate pinning: off, tofu or strict")
	flag.StringVar(&options.pinsPath, "pins", "tunnet-lite-pins.json", "certificate pin store")
}

// applyRouting validates the routing choice and points xray-core at the rule
// sets. The lookup path is a process-wide setting, so it has to be established
// before anything starts.
func applyRouting(options *cliOptions) error {
	mode, err := xcfg.ParseRouteMode(options.routeMode)
	if err != nil {
		return err
	}
	options.supervisor.RouteMode = mode

	dir, err := xcfg.ResolveAssets(options.supervisor.Assets)
	if err != nil && mode == xcfg.RouteSmart {
		return fmt.Errorf("smart routing needs the rule sets: %w\n"+
			"put geoip.dat and geosite.dat in %s, or pass -assets", err, dir)
	}
	return nil
}

// sessionPaths groups everything needed to reach the control plane.
type sessionPaths struct {
	identity   string
	nodes      string
	controlURL string
}

type runConfig struct {
	supervisor  supervisor.Options
	interactive bool
	paths       sessionPaths
	hardening   control.Hardening
}

type refreshConfig struct {
	paths       sessionPaths
	hardening   control.Hardening
	autoApprove bool
	verifyURL   string
}

func execute(ctx context.Context, options cliOptions) error {
	if err := applyRouting(&options); err != nil {
		return err
	}
	hardening, err := buildHardening(options.dohList, options.useECH, options.pinMode, options.pinsPath)
	if err != nil {
		return err
	}
	paths := sessionPaths{options.identity, options.nodesPath, options.controlURL}
	if options.refresh {
		return refreshInventory(ctx, refreshConfig{
			paths: paths, hardening: hardening,
			autoApprove: options.autoApprove, verifyURL: options.verifyURL,
		})
	}
	inv, err := inventory.Load(options.nodesPath)
	if err != nil {
		return err
	}
	if options.dump {
		return dumpConfig(ctx, inv, options.supervisor)
	}
	return run(ctx, inv, runConfig{
		supervisor: options.supervisor, interactive: options.interactive,
		paths: paths, hardening: hardening,
	})
}

func dumpConfig(ctx context.Context, inv *inventory.Inventory, options supervisor.Options) error {
	_, configJSON, err := supervisor.Resolve(ctx, inv, options)
	if err != nil {
		return err
	}
	fmt.Println(string(configJSON))
	return nil
}

// buildHardening turns the transport flags into a configuration. DoH, ECH and
// pinning are separable, but they protect different things: DoH and ECH keep
// the control hostname private, pinning is what makes the directory hard to
// forge. Turning one off does not substitute for another.
func buildHardening(dohList string, useECH bool, pinMode, pinsPath string) (control.Hardening, error) {
	var h control.Hardening
	var err error

	switch dohList {
	case "off":
		if useECH {
			return h, errors.New("-ech=true requires DoH; use -ech=false with -doh=off")
		}
	case "":
		h.Resolver, err = resolver.NewDoH()
		h.ECH = useECH
	default:
		h.Resolver, err = resolver.NewDoH(strings.Split(dohList, ",")...)
		h.ECH = useECH
	}
	if err != nil {
		return h, err
	}

	mode, err := pinning.ParseMode(pinMode)
	if err != nil {
		return h, err
	}
	h.PinMode = mode
	if mode != pinning.ModeOff {
		store, err := pinning.Open(pinsPath)
		if err != nil {
			return h, err
		}
		h.Pins = store
	}
	return h, nil
}

// openSession creates or loads the identity and points it at a hardened
// transport.
func openSession(ctx context.Context, p sessionPaths, h control.Hardening) (*control.Session, bool, error) {
	session, fresh, err := control.Open(p.identity, p.nodes)
	if err != nil {
		return nil, false, err
	}
	session.Client.Endpoint = p.controlURL
	session.Client.ExpectECH = h.ECH

	client, err := control.NewHardenedClient(ctx, p.controlURL, h)
	if err != nil {
		return nil, false, err
	}
	session.Client.HTTP = client
	return session, fresh, nil
}

func run(ctx context.Context, inv *inventory.Inventory, config runConfig) error {
	eng := engine.New()
	defer eng.Stop()

	if _, err := supervisor.Start(ctx, eng, inv, config.supervisor); err != nil {
		return err
	}

	if !config.interactive {
		<-ctx.Done()
		log.Printf("shutting down")
		return nil
	}

	session, err := openExistingSession(ctx, config.paths, config.hardening)
	if err != nil {
		return err
	}
	err = console.New(eng, inv, config.supervisor, session, os.Stdin, os.Stdout).Run(ctx)
	log.Printf("shutting down")
	return err
}

func openExistingSession(ctx context.Context, paths sessionPaths, hardening control.Hardening) (*control.Session, error) {
	_, err := os.Stat(paths.identity)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	session, fresh, err := openSession(ctx, paths, hardening)
	if err != nil {
		return nil, err
	}
	if fresh {
		return nil, errors.New("existing identity unexpectedly reopened as new")
	}
	return session, nil
}

// refreshInventory pulls a fresh directory from the control plane. A brand new
// identity has to be approved once; after that the same identity refreshes
// silently with a sync.
func refreshInventory(ctx context.Context, config refreshConfig) error {
	session, fresh, err := openSession(ctx, config.paths, config.hardening)
	if err != nil {
		return err
	}
	if fresh {
		log.Printf("created a new client identity at %s", config.paths.identity)
	}

	inv, err := session.Refresh(ctx, control.RefreshOptions{
		AutoApprove: config.autoApprove, VerificationURL: config.verifyURL,
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
		config.paths.nodes, len(inv.Hosts), len(inv.EntryGroups), len(inv.RootDomains))
	return nil
}
