#!/usr/bin/env bash
set -euo pipefail

for dependency in Xvfb xclip xsel go; do
    if ! command -v "${dependency}" >/dev/null 2>&1; then
        echo "Missing test dependency: ${dependency}" >&2
        exit 1
    fi
done

x11_test_runtime="$(mktemp -d)"
x11_test_log="${x11_test_runtime}/xvfb.log"
x11_test_display=:99

Xvfb "${x11_test_display}" -screen 0 1280x1024x24 -nolisten tcp -ac >"${x11_test_log}" 2>&1 &
x11_test_pid=$!

cleanup() {
    kill -9 "${x11_test_pid}" 2>/dev/null || true
    wait "${x11_test_pid}" 2>/dev/null || true
    rm -rf -- "${x11_test_runtime}"
}
trap cleanup EXIT

for _ in $(seq 1 50); do
    if [ -S /tmp/.X11-unix/X99 ]; then
        break
    fi
    sleep 0.1
done

if [ ! -S /tmp/.X11-unix/X99 ]; then
    sed -n '1,200p' "${x11_test_log}"
    echo "Headless X11 server did not start" >&2
    exit 1
fi

DISPLAY="${x11_test_display}" MWB_X11_INTEGRATION=1 \
    go test ./internal/clipboard -run '^TestX11ClipboardIntegration$' -count=1
