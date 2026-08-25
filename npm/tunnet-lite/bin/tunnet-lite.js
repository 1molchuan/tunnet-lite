#!/usr/bin/env node
"use strict";

// This is a thin launcher. The actual program is a Go binary, shipped in a
// per-platform package so that installing pulls down one binary rather than
// all six. npm selects the right one through the "os" and "cpu" fields on
// those packages, which is why they are optional dependencies: the ones that
// do not match the host are skipped rather than failing the install.

const { spawnSync } = require("node:child_process");
const path = require("node:path");

const PACKAGES = {
  "darwin arm64": "@1molchuan/tunnet-lite-darwin-arm64",
  "darwin x64": "@1molchuan/tunnet-lite-darwin-x64",
  "linux arm64": "@1molchuan/tunnet-lite-linux-arm64",
  "linux x64": "@1molchuan/tunnet-lite-linux-x64",
  "win32 arm64": "@1molchuan/tunnet-lite-win32-arm64",
  "win32 ia32": "@1molchuan/tunnet-lite-win32-ia32",
  "win32 x64": "@1molchuan/tunnet-lite-win32-x64",
};

function resolveBinary() {
  const key = `${process.platform} ${process.arch}`;
  const pkg = PACKAGES[key];
  if (!pkg) {
    throw new Error(
      `tunnet-lite has no build for ${key}. Supported: ${Object.keys(PACKAGES).join(", ")}.`,
    );
  }

  const exe = process.platform === "win32" ? "tunnet-lite.exe" : "tunnet-lite";
  let manifest;
  try {
    manifest = require.resolve(`${pkg}/package.json`);
  } catch {
    // Reinstalling is the fix in every case worth distinguishing here: an
    // --omit=optional install, a partial install, or a lockfile carried over
    // from a different platform.
    throw new Error(
      `tunnet-lite could not find ${pkg}, which holds the binary for ${key}.\n` +
        `Reinstall without --omit=optional, or install ${pkg} directly.`,
    );
  }
  return path.join(path.dirname(manifest), "bin", exe);
}

let binary;
try {
  binary = resolveBinary();
} catch (error) {
  console.error(error.message);
  process.exit(1);
}

// stdio is inherited so the interactive console works and Ctrl-C reaches the
// program rather than only this launcher.
const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  console.error(`tunnet-lite failed to start: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
