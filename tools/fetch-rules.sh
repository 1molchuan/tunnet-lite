#!/usr/bin/env sh
# Fetch the rule sets smart routing needs.
#
# The files are not vendored: keeping them external means you can see which
# rules are in force, update them on your own schedule, and swap in a different
# source. The trade-off is that you have to fetch them, and a partial download
# is not obviously broken — it fails later with a confusing "EOF" from the geo
# data loader. So every file is checked against its published digest and only
# moved into place once it matches.
set -eu

DEST="${1:-assets}"
BASE="${RULES_BASE_URL:-https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download}"
ATTEMPTS="${RULES_ATTEMPTS:-3}"

mkdir -p "$DEST"
cd "$DEST"

for file in geoip.dat geosite.dat; do
    curl -fsSL --max-time 60 -o "$file.sha256sum" "$BASE/$file.sha256sum"
    want=$(awk '{print $1}' "$file.sha256sum")

    attempt=1
    while [ "$attempt" -le "$ATTEMPTS" ]; do
        curl -fsSL --retry 3 --retry-all-errors --max-time 600 -o "$file.part" "$BASE/$file"
        got=$(sha256sum "$file.part" | awk '{print $1}')
        if [ "$want" = "$got" ]; then
            mv "$file.part" "$file"
            echo "$file ok ($(wc -c < "$file") bytes)"
            break
        fi
        echo "$file: digest mismatch on attempt $attempt" >&2
        attempt=$((attempt + 1))
    done

    if [ ! -f "$file" ] || [ -f "$file.part" ]; then
        rm -f "$file.part"
        echo "$file: could not be fetched intact after $ATTEMPTS attempts" >&2
        exit 1
    fi
done
