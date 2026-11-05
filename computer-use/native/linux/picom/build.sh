#!/usr/bin/env bash
# Build the optional Picom companion for the Linux clean-capture backend.
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
    printf 'Usage: %s output-directory [picom-source-archive]\n' "$0" >&2
    exit 2
fi

here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$1"
output=$(cd "$1" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

archive="$tmp/picom.tar.gz"
if [[ $# -eq 2 ]]; then
    cp "$2" "$archive"
else
    curl --fail --silent --show-error --location --max-time 120 \
        https://deb.debian.org/debian/pool/main/p/picom/picom_9.1.orig.tar.gz \
        --output "$archive"
fi
printf '%s  %s\n' 8700ac71bd496c91262c8576e29cb3aecf2b4ef48c04394a929509d3cb37b87d "$archive" \
    | sha256sum --check --status

mkdir "$tmp/src"
tar -xzf "$archive" -C "$tmp/src" --strip-components=1
patch -d "$tmp/src" -p1 --batch --forward < "$here/clean-capture.patch"
cp "$here/hcu_capture.c" "$here/hcu_capture.h" "$tmp/src/src/"
meson setup "$tmp/build" "$tmp/src" --buildtype=release -Dcompton=false -Dopengl=false -Ddbus=false
meson compile -C "$tmp/build"
install -m 755 "$tmp/build/src/picom" "$output/picom-hcu"
install -d "$output/picom-licenses"
cp "$tmp/src/LICENSE.spdx" "$tmp/src/LICENSES/"* "$output/picom-licenses/"
