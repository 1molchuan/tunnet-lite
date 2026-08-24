// Package console is an interactive terminal front end.
//
// It deliberately has no dependencies and no network surface: it reads commands
// from stdin and drives the engine directly. That keeps the binary usable over
// a plain SSH session and avoids exposing a local HTTP API.
package console

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/1molchuan/tunnet-lite/internal/control"
	"github.com/1molchuan/tunnet-lite/internal/engine"
	"github.com/1molchuan/tunnet-lite/internal/inventory"
	"github.com/1molchuan/tunnet-lite/internal/supervisor"
)

// Console owns the mutable choices the operator edits between restarts.
type Console struct {
	engine  *engine.Engine
	session *control.Session // optional; nil disables "refresh"

	inv  *inventory.Inventory
	opts supervisor.Options

	in  *bufio.Scanner
	out *bufio.Writer
}

func New(eng *engine.Engine, inv *inventory.Inventory, opts supervisor.Options,
	session *control.Session, in io.Reader, out io.Writer) *Console {
	return &Console{
		engine: eng, session: session, inv: inv, opts: opts,
		in:  bufio.NewScanner(in),
		out: bufio.NewWriter(out),
	}
}

type command struct {
	name    string
	args    string
	summary string
	run     func(ctx context.Context, c *Console, arg string) error
}

// commands is populated in init because cmdHelp reads the table it belongs to.
var commands []command

func init() {
	commands = []command{
		{"status", "", "show what is running", cmdStatus},
		{"exits", "", "list exit nodes", cmdExits},
		{"entries", "", "list operator ingresses", cmdEntries},
		{"exit", "<slug|#>", "select the exit node", cmdSetExit},
		{"entry", "<name|#>", "select the operator ingress", cmdSetEntry},
		{"udp", "on|off", "toggle SOCKS UDP associate", cmdUDP},
		{"direct", "on|off", "bypass the front proxy", cmdDirect},
		{"start", "", "apply the current selection", cmdStart},
		{"stop", "", "stop the proxy", cmdStop},
		{"refresh", "", "fetch a fresh node directory", cmdRefresh},
		{"help", "", "show this list", cmdHelp},
	}
}

// Run reads commands until stdin closes or ctx is cancelled.
func (c *Console) Run(ctx context.Context) error {
	c.printf("tunnet-lite console. Type \"help\" for commands, \"quit\" to leave.\n\n")
	c.showStatus()

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		c.printf("\n> ")
		c.out.Flush()
		if !c.in.Scan() {
			c.printf("\n")
			c.out.Flush()
			return c.in.Err()
		}

		line := strings.TrimSpace(c.in.Text())
		if line == "" {
			continue
		}
		name, arg, _ := strings.Cut(line, " ")
		name = strings.ToLower(name)
		arg = strings.TrimSpace(arg)

		if name == "quit" || name == "q" {
			return nil
		}
		if err := c.dispatch(ctx, name, arg); err != nil {
			c.printf("error: %v\n", err)
		}
		c.out.Flush()
	}
}

func (c *Console) dispatch(ctx context.Context, name, arg string) error {
	for _, cmd := range commands {
		if cmd.name == name {
			return cmd.run(ctx, c, arg)
		}
	}
	return fmt.Errorf("unknown command %q; type \"help\"", name)
}

func cmdHelp(_ context.Context, c *Console, _ string) error {
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	for _, cmd := range commands {
		fmt.Fprintf(w, "  %s %s\t%s\n", cmd.name, cmd.args, cmd.summary)
	}
	fmt.Fprintf(w, "  quit\t leave the console; the proxy stops with it\n")
	return w.Flush()
}

func cmdStatus(_ context.Context, c *Console, _ string) error {
	c.showStatus()
	return nil
}

func (c *Console) showStatus() {
	st := c.engine.Status()
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	if !st.Running {
		fmt.Fprintf(w, "state\tstopped\n")
		if st.LastError != "" {
			fmt.Fprintf(w, "last error\t%s\n", st.LastError)
		}
	} else {
		p := st.Plan
		network := "tcp only"
		if p.UDP {
			network = "tcp+udp"
		}
		front := "direct"
		if p.FrontProxy != "" {
			front = p.FrontProxy
		}
		fmt.Fprintf(w, "state\trunning since %s\n", st.StartedAt.Format(time.TimeOnly))
		fmt.Fprintf(w, "listening\tsocks5://%s:%d (%s)\n", p.Listen, p.Port, network)
		fmt.Fprintf(w, "exit\t%s — %s\n", p.HostSlug, p.HostName)
		fmt.Fprintf(w, "host name\t%s\n", p.LogicalHost)
		fmt.Fprintf(w, "entry\t%s\n", p.GroupName)
		fmt.Fprintf(w, "front proxy\t%s\n", front)
		fmt.Fprintf(w, "pool\t%d addresses, best %s\n", len(p.Entries), p.Entries[0])
	}
	fmt.Fprintf(w, "selection\texit=%s entry=%s udp=%s direct=%s\n",
		orDefault(c.opts.HostSlug, "(first online)"),
		orDefault(c.opts.EntryGroup, "(first)"),
		onOff(c.opts.UDP), onOff(c.opts.NoFront))
	w.Flush()
}

func cmdExits(_ context.Context, c *Console, _ string) error {
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	for i, h := range c.inv.Hosts {
		state := "online"
		if !h.Online {
			state = "offline"
		}
		fmt.Fprintf(w, "%s %2d\t%s\t%s\t%s\n", selected(h.Slug == c.opts.HostSlug), i+1, h.Slug, h.Name, state)
	}
	return w.Flush()
}

func cmdEntries(_ context.Context, c *Console, _ string) error {
	w := tabwriter.NewWriter(c.out, 0, 0, 2, ' ', 0)
	for i, g := range c.inv.EntryGroups {
		front := "no front proxy"
		if g.FrontProxy != nil {
			front = g.FrontProxy.Endpoint
		}
		fmt.Fprintf(w, "%s %2d\t%s\t%d addresses\t%s\n",
			selected(g.Name == c.opts.EntryGroup), i+1, g.Name, len(g.IPv4), front)
	}
	return w.Flush()
}

func cmdSetExit(_ context.Context, c *Console, arg string) error {
	if arg == "" {
		return errors.New("usage: exit <slug|#>")
	}
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(c.inv.Hosts) {
			return fmt.Errorf("no exit number %d; try \"exits\"", n)
		}
		arg = c.inv.Hosts[n-1].Slug
	}
	host, err := c.inv.SelectHost(arg)
	if err != nil {
		return err
	}
	c.opts.HostSlug = host.Slug
	c.printf("exit set to %s — %s. Run \"start\" to apply.\n", host.Slug, host.Name)
	return nil
}

func cmdSetEntry(_ context.Context, c *Console, arg string) error {
	if arg == "" {
		return errors.New("usage: entry <name|#>")
	}
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(c.inv.EntryGroups) {
			return fmt.Errorf("no entry number %d; try \"entries\"", n)
		}
		arg = c.inv.EntryGroups[n-1].Name
	}
	group, err := c.inv.SelectGroup(arg)
	if err != nil {
		return err
	}
	c.opts.EntryGroup = group.Name
	c.printf("entry set to %s. Run \"start\" to apply.\n", group.Name)
	return nil
}

func cmdUDP(_ context.Context, c *Console, arg string) error {
	on, err := parseToggle(arg, c.opts.UDP)
	if err != nil {
		return err
	}
	c.opts.UDP = on
	c.printf("udp %s. Run \"start\" to apply.\n", onOff(on))
	return nil
}

func cmdDirect(_ context.Context, c *Console, arg string) error {
	on, err := parseToggle(arg, c.opts.NoFront)
	if err != nil {
		return err
	}
	c.opts.NoFront = on
	state := "in use"
	if on {
		state = "bypassed"
	}
	c.printf("front proxy %s. Run \"start\" to apply.\n", state)
	return nil
}

func cmdStart(ctx context.Context, c *Console, _ string) error {
	c.printf("probing entry pool…\n")
	c.out.Flush()
	if _, err := supervisor.Start(ctx, c.engine, c.inv, c.opts); err != nil {
		return err
	}
	c.showStatus()
	return nil
}

func cmdStop(_ context.Context, c *Console, _ string) error {
	if err := c.engine.Stop(); err != nil {
		return err
	}
	c.printf("stopped\n")
	return nil
}

func cmdRefresh(ctx context.Context, c *Console, _ string) error {
	if c.session == nil {
		return errors.New("no control-plane identity; run once with -refresh to create one")
	}
	c.printf("contacting the control plane…\n")
	c.out.Flush()

	inv, err := c.session.Refresh(ctx, control.RefreshOptions{})
	var needsAuth *control.NeedsAuthorizationError
	if errors.As(err, &needsAuth) {
		c.printf("this client is not approved yet.\n  open: %s\n  then run \"refresh\" again\n",
			needsAuth.VerificationURL)
		return nil
	}
	if err != nil {
		return err
	}

	c.inv = inv
	c.printf("updated: %d exits, %d ingresses, %d root domains\n",
		len(inv.Hosts), len(inv.EntryGroups), len(inv.RootDomains))
	return nil
}

func parseToggle(arg string, current bool) (bool, error) {
	switch strings.ToLower(arg) {
	case "on", "yes", "true", "1":
		return true, nil
	case "off", "no", "false", "0":
		return false, nil
	case "", "toggle":
		return !current, nil
	}
	return false, fmt.Errorf("expected on or off, got %q", arg)
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func selected(b bool) string {
	if b {
		return "*"
	}
	return " "
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (c *Console) printf(format string, args ...any) {
	fmt.Fprintf(c.out, format, args...)
}
