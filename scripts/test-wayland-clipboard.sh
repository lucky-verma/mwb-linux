#!/usr/bin/env bash
set -euo pipefail

for dependency in sway wl-copy wl-paste go; do
    if ! command -v "${dependency}" >/dev/null 2>&1; then
        echo "Missing test dependency: ${dependency}" >&2
        exit 1
    fi
done

wayland_test_runtime="$(mktemp -d)"
wayland_test_log="${wayland_test_runtime}/sway.log"
chmod 700 "${wayland_test_runtime}"

export XDG_RUNTIME_DIR="${wayland_test_runtime}"
export WLR_BACKENDS=headless
export WLR_RENDERER=pixman
export WLR_LIBINPUT_NO_DEVICES=1

sway --unsupported-gpu -c /dev/null >"${wayland_test_log}" 2>&1 &
wayland_test_pid=$!

cleanup() {
    kill "${wayland_test_pid}" 2>/dev/null || true
    wait "${wayland_test_pid}" 2>/dev/null || true
    rm -rf -- "${wayland_test_runtime}"
}
trap cleanup EXIT

wayland_test_socket=""
for _ in $(seq 1 50); do
    wayland_test_socket="$(find "${wayland_test_runtime}" -maxdepth 1 -type s -name 'wayland-*' -printf '%f\n' -quit)"
    if [ -n "${wayland_test_socket}" ]; then
        break
    fi
    sleep 0.1
done

if [ -z "${wayland_test_socket}" ]; then
    sed -n '1,200p' "${wayland_test_log}"
    echo "Headless Wayland compositor did not start" >&2
    exit 1
fi

export WAYLAND_DISPLAY="${wayland_test_socket}"
MWB_WAYLAND_INTEGRATION=1 go test ./internal/clipboard \
    -run '^TestWaylandClipboardIntegration$' -count=1
