#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
    printf 'Usage: %s output-directory [picom-source-archive]\n' "$0" >&2
    exit 2
fi

case "$(uname -sm)" in
    'Linux x86_64') arch=amd64 ;;
    'Linux aarch64') arch=arm64 ;;
    *) printf 'Packaging requires Linux amd64 or arm64.\n' >&2; exit 2 ;;
esac

root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$1"
output=$(cd "$1" && pwd)
version=$(cat "$root/VERSION")
package="hcu-$version-linux-$arch"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
bundle="$tmp/$package"

picom_source=("${@:2}")
"$root/computer-use/native/linux/picom/build.sh" "$bundle" "${picom_source[@]}"
(
    cd "$root/computer-use"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -mod=vendor -buildvcs=false -trimpath \
        -ldflags "-s -w -X main.version=$version" \
        -o "$bundle/computer-use" ./cmd/computer-use
)

cp "$root/VERSION" "$root/LICENSE" "$root/THIRD_PARTY_NOTICES.md" "$bundle/"
mkdir "$bundle/licenses"
cp "$root"/licenses/{MIT-cua-cursor,Apache-2.0,Playwright-NOTICE}.txt "$bundle/licenses/"
vendor="$root/computer-use/vendor"
cp "$vendor/github.com/jezek/xgb/LICENSE" "$bundle/licenses/xgb.txt"
cp "$vendor/github.com/santhosh-tekuri/jsonschema/v6/LICENSE" "$bundle/licenses/jsonschema.txt"
cp "$vendor/golang.org/x/text/LICENSE" "$bundle/licenses/x-text.txt"
cp "$vendor/golang.org/x/text/PATENTS" "$bundle/licenses/x-text-patents.txt"

tar -czf "$output/$package.tar.gz" -C "$tmp" "$package"
