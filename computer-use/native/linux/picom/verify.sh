#!/usr/bin/env bash
# Exercise the companion in a disposable Xvfb desktop.
set -euo pipefail
picom=$(realpath "${1:?Usage: verify.sh path/to/picom-hcu}")
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
server=
compositor=
cleanup() {
    if [[ -n $compositor ]]; then kill "$compositor" 2>/dev/null || true; fi
    if [[ -n $server ]]; then kill "$server" 2>/dev/null || true; fi
    wait || true
    rm -rf "$tmp"
}
trap cleanup EXIT

Xvfb -displayfd 3 -screen 0 1024x600x24 -nolisten tcp -ac \
    3>"$tmp/display" >"$tmp/xvfb.log" 2>&1 &
server=$!
for ((i = 0; i < 50; i++)); do
    if [[ -s $tmp/display ]]; then break; fi
    sleep .05
done
if [[ ! -s $tmp/display ]]; then
    cat "$tmp/xvfb.log" >&2
    exit 1
fi
export DISPLAY=":$(cat "$tmp/display")"
export HCU_CLEAN_CAPTURE_SOCKET="$tmp/capture.sock"

start_compositor() {
    "$picom" --config /dev/null --backend xrender -f -I 1 -O .01 -D 10 >"$tmp/picom.log" 2>&1 &
    compositor=$!
    for ((i = 0; i < 50; i++)); do
        if python3 -c 'import os, socket; s=socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM); s.connect(os.environ["HCU_CLEAN_CAPTURE_SOCKET"])' 2>/dev/null; then
            return
        fi
        sleep .05
    done
    cat "$tmp/picom.log" >&2
    return 1
}
start_compositor
timeout 15s python3 "$here/test_clean_capture.py"

# A forced kill leaves a stale socket inode. The next session must reclaim
# it safely and serve fresh frames.
kill -KILL "$compositor"
wait "$compositor" 2>/dev/null || true
compositor=
start_compositor
timeout 15s python3 "$here/test_clean_capture.py"
