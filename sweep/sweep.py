#!/usr/bin/env python3
"""Re-validate the VLESS Encryption wire parameters against a live node.

This is the tool that established the current defaults. The reference client was
first assumed to use xorMode "xorpub"; sweeping proved it is "native", which was
the single reason connections were being reset. Whenever xray-core is upgraded,
or a connection that used to work starts failing at the handshake, run this
before changing anything else -- it tells you whether the wire format moved.

  python3 sweep/sweep.py --binary ./tunnet-lite.exe --nodes nodes.json

A row is a pass only if traffic actually reaches the exit. Secrets are never
printed: the tool only ever passes the inventory path to the binary.
"""

import argparse
import itertools
import re
import shutil
import subprocess
import sys
import time

CURL_CANDIDATES = ["/mnt/c/Windows/System32/curl.exe", "curl"]
CHECK_URL = "https://api.ipify.org"


def find_curl():
    for c in CURL_CANDIDATES:
        if shutil.which(c) or c.startswith("/mnt/"):
            return c
    sys.exit("no curl available")


def run_case(args, curl, mode, rtt, padding, flow):
    cmd = [
        args.binary, "-nodes", args.nodes, "-port", str(args.port),
        "-enc-mode", mode, "-enc-rtt", rtt, "-enc-padding", padding,
        "-flow", flow, "-log-level", "warning",
        "-state", args.state, "-max-entries", str(args.max_entries),
    ]
    if args.root:
        cmd += ["-root", args.root]
    if args.host:
        cmd += ["-host", args.host]
    if args.no_front:
        cmd += ["-no-front"]

    log = open(args.log, "w")
    proc = subprocess.Popen(cmd, stdout=log, stderr=subprocess.STDOUT)
    time.sleep(args.warmup)

    ok = 0
    detail = ""
    for _ in range(args.attempts):
        r = subprocess.run(
            [curl, "-s", "-S", "--proxy", f"socks5h://127.0.0.1:{args.port}",
             "--max-time", str(args.timeout), CHECK_URL],
            capture_output=True, text=True)
        out = (r.stdout or r.stderr).strip()
        if r.returncode == 0 and "curl:" not in out and out:
            ok += 1
            detail = out
        else:
            detail = out.replace("\n", " ")[:48]
        time.sleep(0.5)

    proc.terminate()
    try:
        proc.wait(5)
    except Exception:
        proc.kill()
    log.close()

    handshake = ""
    try:
        text = open(args.log, errors="replace").read()
        if re.search(r"connection ends|Connection was reset|handshake", text, re.I):
            handshake = "server rejected"
    except OSError:
        pass
    return ok, detail, handshake


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--binary", default="./tunnet-lite.exe")
    p.add_argument("--nodes", default="nodes.json")
    p.add_argument("--port", type=int, default=18080,
                   help="isolated SOCKS port; keep it away from any port already in use")
    p.add_argument("--root", default="")
    p.add_argument("--host", default="")
    p.add_argument("--no-front", action="store_true")
    p.add_argument("--state", default="sweep-state.json")
    p.add_argument("--log", default="sweep.log")
    p.add_argument("--warmup", type=float, default=6)
    p.add_argument("--attempts", type=int, default=2)
    p.add_argument("--timeout", type=int, default=25)
    p.add_argument("--max-entries", type=int, default=2,
                   help="small pool keeps each case fast")
    p.add_argument("--modes", default="native,xorpub,random")
    p.add_argument("--rtts", default="1rtt,0rtt")
    p.add_argument("--paddings", default="100-35-35")
    p.add_argument("--flows", default="xtls-rprx-vision,none")
    args = p.parse_args()

    curl = find_curl()
    combos = list(itertools.product(
        args.modes.split(","), args.rtts.split(","),
        args.paddings.split(","), args.flows.split(",")))

    print(f"{len(combos)} combinations, {args.attempts} attempts each\n")
    print(f"{'mode':<8}{'rtt':<7}{'padding':<12}{'flow':<18}{'pass':<7}note")
    print("-" * 72)
    passes = []
    for mode, rtt, padding, flow in combos:
        ok, detail, note = run_case(args, curl, mode, rtt, padding, flow)
        label = flow
        verdict = f"{ok}/{args.attempts}"
        if ok == args.attempts:
            passes.append((mode, rtt, padding, flow))
            note = f"exit {detail}"
        print(f"{mode:<8}{rtt:<7}{padding:<12}{label:<18}{verdict:<7}{note}")

    print()
    if passes:
        print("working combinations:")
        for c in passes:
            print("  mlkem768x25519plus.%s.%s.%s  flow=%s" % (c[0], c[1], c[2], c[3] or "(none)"))
    else:
        print("no combination worked: the inventory may be stale, or the wire format moved")
        sys.exit(1)


if __name__ == "__main__":
    main()
