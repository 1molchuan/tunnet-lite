# tunnet-lite

An open-source backend for the TunNet data plane: a local SOCKS proxy that
speaks the same tunnel the vendor client speaks, built on `xray-core` used as a
library, with an interactive terminal console for switching routes.

```
SOCKS 127.0.0.1:18080
  -> VLESS (VLESS Encryption native/1-RTT, XTLS Vision)
  -> XHTTP stream-up (GET downlink / POST uplink)
  -> HTTP/2 over TLS 1.3 + ECH (uTLS Chrome 133)
  -> operator front proxy (HTTP CONNECT)  [optional]
  -> CDN edge -> exit node
```

Nothing is compiled in. The account identity, the per-host encryption keys, the
rotating root domains and the operator ingress pools all come from the control
plane at run time, or from an inventory file you supply.

No GUI, no web server, no external UI dependencies: the console reads commands
from stdin, so the binary works the same over SSH as it does locally.

## Quick start

```bash
go build -o tunnet-lite ./

# One-time: create an identity and fetch the node directory.
# The first run prints a verification URL; approve it, then run again.
./tunnet-lite -refresh

./tunnet-lite -console
```

```
> exits
   1  tyo-01         Tokyo 01     online
   7  sin-01         Singapore    online
> exit sin-01
exit set to sin-01 — Singapore. Run "start" to apply.
> start
state        running since 22:49:33
listening    socks5://127.0.0.1:18080 (tcp only)
exit         sin-01 — Singapore
```

Without `-console` the binary starts the proxy and serves until interrupted,
which is what you want under a service manager.

`nodes.json` and `tunnet-lite-identity.json` carry your account credential and
the node keys. Both are in `.gitignore` and must stay out of version control.

| Flag | Meaning |
|---|---|
| `-console` | interactive console instead of just serving |
| `-refresh` | fetch the directory from the control plane, then exit |
| `-host tyo-01` | pick the exit slug (default: first online host) |
| `-entry-group 电信` | pick the operator ingress by name or substring |
| `-no-front` | dial the CDN directly, skipping the front proxy |
| `-root <domain>` | pin a root domain instead of using the cached choice |
| `-dump-config` | print the rendered Xray config and exit |
| `-port 18080` | SOCKS port |

## Control plane

`-refresh` runs the signed, encrypted control-plane exchange:

1. **bootstrap** — announces the identity. A new identity comes back with a
   ticket and a verification URL.
2. **approve** — a human opens that URL. The URL lives on a rotating root
   domain, so it is read out of the response rather than hardcoded.
   `-auto-approve` calls the same endpoint the page calls, which only makes
   sense for your own account.
3. **access** — exchanges the approved ticket for the full directory.
4. **sync** — refreshes an already approved identity on later runs.

Requests are signed with RFC 9421 HTTP Message Signatures over method,
authority, path, content type, digest and both key headers. Responses are HPKE
sealed to a fresh X25519 key generated per request, bound to the operation, the
client id and the request nonce, so terminating TLS in between does not reveal
the directory.

The two responses differ in shape, which matters:

| | access | sync |
|---|---|---|
| envelope | `bootstrap.runtime` | `runtime` (top level) |
| hosts, entry pools | yes | yes |
| `network.root_domains` | yes | **no** |

A sync is therefore a partial update and is merged onto what is already known.
This is also why the root domain is cached locally: the pool only ever arrives
with a full access response.

## How entry selection works

Two mechanisms, deliberately:

1. **Startup: TCP RTT ranking.** Every address in the chosen ingress pool is
   dialled concurrently. Unreachable addresses are dropped, the rest are ordered
   by RTT, and the fastest becomes the balancer's `fallbackTag`. This is cheap
   and matches what the vendor client does (`measureTCPRTT`).
2. **Runtime: Xray health checks.** All surviving addresses become pool members
   behind a `leastPing` balancer driven by `burstObservatory`, so a degrading
   entry is abandoned without a restart.

Two health-check settings are load-bearing and are enforced in code:

- **`interval` must exceed `timeout`.** Overlapping rounds cancel each other and
  then *every* entry reports `io: read/write on closed pipe`, healthy ones
  included.
- **`timeout` must be generous.** Xray's 5 s default cannot complete a probe on
  this tunnel, because `burstObservatory` disables keep-alives and every probe
  therefore builds a complete VLESS Encryption + XHTTP + TLS/ECH session. The
  default here is 15 s.

## UDP does not traverse these nodes

`-udp` wires SOCKS UDP associate, and the listener does accept and route UDP
locally — but nothing comes back, and this is a property of the operator's
nodes rather than a gap here.

The chain is forced: XTLS Vision is mandatory on this service (see below), and
Xray routes **every** UDP association through mux when Vision is active. These
nodes do not answer mux at all — enabling mux for a plain TCP request fails the
same way, which is the test that isolates it.

Use `socks5h://` so names are resolved at the proxy over TCP. That is what the
vendor works around too: its client ships two hardcoded DoH upstreams rather
than relying on UDP through the tunnel.

The flag is kept, with a warning on every start, so the limitation can be
re-tested if the operator's configuration changes.

## Wire parameters

Established by sweeping them against a live node, not by reading code.

| Parameter | Value | Evidence |
|---|---|---|
| xorMode | `native` (0) | `xorpub` and `random` are both rejected: the server closes both HTTP/2 streams right after the 1333-byte client hello |
| handshake | `1rtt` | `0rtt` behaves identically on a first connection |
| padding | `100-35-35` | yields a single 35-byte pad, i.e. the 1333-byte hello |
| flow | `xtls-rprx-vision` | **required**: 4/4 with it, 0/4 without |
| XHTTP mode | `stream-up` | GET downlink, POST uplink with `Content-Type: application/grpc` |
| TLS | uTLS Chrome 133, ALPN `h2`, ECH | ECH public name comes from the host's HTTPS record |
| mux | unsupported by the nodes | mux-enabled TCP fails; this is what blocks UDP |

`native` is the parameter that matters most. An earlier analysis inferred
`xorpub` from disassembly and every connection was reset; sweeping the three
values is what found the error.

`flow` has a cautionary history worth keeping. It was briefly recorded as
optional after a sweep showed identical results with and without it. That sweep
was wrong: an empty `Flow` meant "use the default", so the supposedly
flow-less rows were running Vision too. Disabling it needs the explicit
`-flow none` token, and once that existed the answer came back unambiguous.
The lesson generalises — when a sweep says a parameter does not matter, check
that the tool can actually express the negative case.

## Pinned dependency, and how to upgrade it

`go.mod` pins `xray-core` to a specific commit through a `replace` to a fork:

```
require github.com/xtls/xray-core v0.0.0-20260824000000-f02a35786124
replace github.com/xtls/xray-core => ../xray-tunnet
```

The fork is upstream commit `f02a35786124a6ad046727f2408e32317cc19a41` plus two
fixes described below. Point the `replace` at your published fork when you
publish this.

**Never bump `xray-core` casually.** VLESS Encryption is a young, fast-moving
feature. The vendor server is a frozen fork of some upstream revision; today's
upstream happens to agree with it on the 1333-byte handshake, and that agreement
is a property of this moment, not a guarantee.

Treat an upgrade as a change that must be re-validated:

```bash
go build -o tunnet-lite ./
python3 sweep/sweep.py --nodes nodes.json --attempts 2
```

Only if a row passes, adopt its parameters as the new defaults in
`internal/xcfg/xcfg.go` and record the evidence here. If nothing passes, the
wire format moved: roll the pin back rather than guessing at parameters.

## The two xray-core patches

Both live in `proxy/http/client.go`, both are generic bugs unrelated to this
project, and both are worth submitting upstream so the fork can go away.

**1. `headers` cannot override `Host` on CONNECT.** Go's `Request.Write`
excludes `Header["Host"]` and emits `req.Host` instead, so a configured `Host`
never reached the wire. Redirecting it to `req.Host` is not enough either: for
CONNECT, Go also derives the request-URI from `req.Host`, which rewrites the
tunnel authority — the symptom is a TLS handshake that lands on the front
proxy's own certificate. The fix pins the real target in `req.URL.Opaque` first.

Without this, an operator front proxy that authenticates on a `Host` override
cannot be used at all.

**2. An outbound with `headers` cannot be health-checked.**
`fillRequestHeader` required inbound session metadata whenever any header was
configured, but `burstObservatory` dispatches through `tagged.Dialer`, which
carries no inbound. Every probe of such an outbound failed immediately with
`io: read/write on closed pipe`, so `leastPing` never had data and the balancer
fell back permanently. The fix only requires metadata when a header value
actually contains a `{{` template.

Without this, entry failover silently does nothing whenever a front proxy is
configured.

## Layout

| Path | Responsibility |
|---|---|
| `internal/control` | signed and HPKE-sealed control-plane calls, identity persistence |
| `internal/inventory` | node inventory loading, validation, selection, partial merges |
| `internal/probe` | concurrent TCP RTT ranking of the entry pool |
| `internal/xcfg` | renders the Xray config; holds the validated wire parameters |
| `internal/engine` | owns the running xray-core instance; restartable |
| `internal/supervisor` | resolves choices, ranks entries, applies a plan |
| `internal/console` | interactive terminal front end, no dependencies |
| `tools/access2nodes` | converts an already-decrypted response to an inventory |
| `sweep/` | wire-parameter re-validation |

## Not implemented

- **TUN.** `xray-core` has no built-in TUN; the vendor client uses gVisor. Route
  traffic through the SOCKS listener, or bring your own tun2socks.
- **UDP end to end.** See above; blocked by the nodes, not by this code.

## Licence and use

This is an interoperability client. It expects *your* credentials. Do not
publish an inventory or an identity file: `client_id` is an account-level
credential, and shipping it with a node directory redistributes access to a
paid service rather than sharing an implementation.
