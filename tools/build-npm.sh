#!/usr/bin/env sh
# Assemble the npm packages: one launcher package plus one package per platform.
#
# The binaries are split out so that installing fetches a single ~31 MB build
# rather than all six. npm picks the right one from the "os" and "cpu" fields,
# which only works if they are optional dependencies — a non-matching one is
# then skipped instead of failing the install.
set -eu

VERSION="${1:?usage: build-npm.sh <version> [outdir]}"
OUT="${2:-dist/npm}"
SCOPE="@1molchuan"
GO="${GO:-go}"

# GOOS GOARCH npm-platform npm-arch
TARGETS="
darwin arm64 darwin arm64
darwin amd64 darwin x64
linux arm64 linux arm64
linux amd64 linux x64
windows arm64 win32 arm64
windows amd64 win32 x64
windows 386 win32 ia32
"

rm -rf "$OUT"
mkdir -p "$OUT"

# Build each platform package.
echo "$TARGETS" | while read -r goos goarch nodeos nodearch; do
    [ -n "$goos" ] || continue

    name="tunnet-lite-$nodeos-$nodearch"
    dir="$OUT/$name"
    exe="tunnet-lite"
    [ "$goos" = "windows" ] && exe="tunnet-lite.exe"

    mkdir -p "$dir/bin"
    # stdin is redirected because the loop reads from a pipe: a build tool that
    # touches stdin would otherwise swallow the remaining targets.
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        "$GO" build -trimpath -ldflags="-s -w" -o "$dir/bin/$exe" . < /dev/null
    chmod +x "$dir/bin/$exe"

    # The binary is the combined work that links MPL-covered xray-core, so the
    # licence and the attribution belong with it, not only with the launcher.
    [ -f LICENSE ] && cp LICENSE "$dir/"
    [ -f NOTICE ] && cp NOTICE "$dir/"

    cat > "$dir/package.json" <<JSON
{
  "name": "$SCOPE/$name",
  "version": "$VERSION",
  "description": "tunnet-lite binary for $nodeos $nodearch",
  "license": "MIT",
  "repository": { "type": "git", "url": "git+https://github.com/1molchuan/tunnet-lite.git" },
  "os": ["$nodeos"],
  "cpu": ["$nodearch"],
  "files": ["bin/", "NOTICE"]
}
JSON
    echo "built $SCOPE/$name ($(wc -c < "$dir/bin/$exe") bytes)"
done

# Build the launcher package, depending on every platform package optionally.
dir="$OUT/tunnet-lite"
mkdir -p "$dir/bin"
cp npm/tunnet-lite/bin/tunnet-lite.js "$dir/bin/"
chmod +x "$dir/bin/tunnet-lite.js"
[ -f README.md ] && cp README.md "$dir/"
[ -f README.zh-CN.md ] && cp README.zh-CN.md "$dir/"
[ -f LICENSE ] && cp LICENSE "$dir/"
[ -f NOTICE ] && cp NOTICE "$dir/"

deps=$(echo "$TARGETS" | awk -v scope="$SCOPE" -v ver="$VERSION" '
    NF { printf "%s    \"%s/tunnet-lite-%s-%s\": \"%s\"", sep, scope, $3, $4, ver; sep = ",\n" }
    END { printf "\n" }')

cat > "$dir/package.json" <<JSON
{
  "name": "tunnet-lite",
  "version": "$VERSION",
  "description": "Client for the TunNet data plane, built on xray-core",
  "license": "MIT",
  "repository": { "type": "git", "url": "git+https://github.com/1molchuan/tunnet-lite.git" },
  "bin": { "tunnet-lite": "bin/tunnet-lite.js" },
  "files": ["bin/", "NOTICE"],
  "engines": { "node": ">=18" },
  "optionalDependencies": {
$deps  }
}
JSON

echo "assembled $OUT"
