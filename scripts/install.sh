#!/bin/bash
set -euo pipefail

REPO="lucky-verma/mwb-linux"
VERSION="${MWB_VERSION:-latest}"
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="mwb"

echo "=== MWB Linux Installer ==="
echo ""

if [ "${EUID}" -ne 0 ]; then
    echo "Please run with sudo: curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh | sudo bash"
    exit 1
fi

ACTUAL_USER="${SUDO_USER:-$USER}"
USER_HOME="$(getent passwd "${ACTUAL_USER}" | cut -d: -f6)"
if [ -z "${USER_HOME}" ]; then
    echo "Could not determine home directory for ${ACTUAL_USER}"
    exit 1
fi

case "$(uname -m)" in
    x86_64 | amd64)
        ARCH="amd64"
        ;;
    aarch64 | arm64)
        ARCH="arm64"
        ;;
    *)
        echo "Unsupported architecture: $(uname -m)"
        exit 1
        ;;
esac

if [ "${VERSION}" = "latest" ]; then
    DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/mwb-linux-${ARCH}"
else
    DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/mwb-linux-${ARCH}"
fi

echo "[1/6] Installing dependencies..."
apt-get update -qq
apt-get install -y -qq ca-certificates curl xdotool xclip wl-clipboard x11-xserver-utils > /dev/null
echo "  Done."

echo "[2/6] Configuring input permissions..."
modprobe uinput
echo 'uinput' > /etc/modules-load.d/uinput.conf
cat > /etc/udev/rules.d/99-mwb-uinput.rules << 'EOF'
KERNEL=="uinput", GROUP="input", MODE="0660"
EOF
cat > /etc/udev/rules.d/99-mwb-libinput.rules << 'EOF'
# mwb virtual mouse: tell libinput not to apply pointer acceleration.
# The device receives absolute coordinates over the network; bursty
# packet timing produces variable input deltas. libinput's default
# adaptive accel curve amplifies that variance into visible cursor
# wobble on Wayland (issue #5). Flat profile = linear 1:1 mapping.
ACTION=="add|change", SUBSYSTEM=="input", ATTRS{name}=="mwb-mouse", ENV{LIBINPUT_ACCEL_PROFILE}="flat", ENV{LIBINPUT_ACCEL_SPEED}="0"
EOF
udevadm control --reload-rules
udevadm trigger /dev/uinput 2>/dev/null || true
echo "  Done."

echo "[3/6] Adding ${ACTUAL_USER} to input group..."
usermod -aG input "${ACTUAL_USER}"
echo "  Done."

echo "[4/6] Installing mwb ${VERSION} (${ARCH})..."
TMP_FILE="$(mktemp)"
trap 'rm -f "${TMP_FILE}"' EXIT
curl -fsSL "${DOWNLOAD_URL}" -o "${TMP_FILE}"
install -D -m 755 "${TMP_FILE}" "${INSTALL_DIR}/${BINARY_NAME}"
echo "  Installed to ${INSTALL_DIR}/${BINARY_NAME}"

echo "[5/6] Creating config template..."
CONFIG_DIR="${USER_HOME}/.config/mwb"
mkdir -p "${CONFIG_DIR}"
if [ ! -f "${CONFIG_DIR}/config.toml" ]; then
    cat > "${CONFIG_DIR}/config.toml" << 'EOF'
# MWB Linux Configuration
# Get the security key from PowerToys -> Mouse Without Borders on Windows.

host = "192.168.1.100"        # Windows machine IP address
key = "YourSecurityKey"       # Security key from PowerToys MWB
name = "linux"                # This machine's name (max 15 chars)
# port = 15100                # Base port (default 15100)
# accel_multiplier = 2.0      # Linux -> Windows cursor speed
# inbound_multiplier = 1.0    # Windows -> Linux cursor speed
EOF
    echo "  Config template created at ${CONFIG_DIR}/config.toml"
else
    echo "  Config already exists at ${CONFIG_DIR}/config.toml"
fi
# The config holds the plaintext security key — keep it owner-only so no other
# local account can read it (0700 dir, 0600 file).
chown "${ACTUAL_USER}:${ACTUAL_USER}" "${CONFIG_DIR}"
chmod 700 "${CONFIG_DIR}"
chown "${ACTUAL_USER}:${ACTUAL_USER}" "${CONFIG_DIR}/config.toml"
chmod 600 "${CONFIG_DIR}/config.toml"

echo "[6/6] Installing systemd user service..."
if command -v systemctl >/dev/null 2>&1 && \
    systemctl list-unit-files --no-legend mwb-linux.service 2>/dev/null | grep -q '^mwb-linux\.service'; then
    echo "  Removing legacy root mwb-linux.service..."
    systemctl disable --now mwb-linux.service >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/mwb-linux.service
    rm -rf /etc/systemd/system/mwb-linux.service.d
    systemctl daemon-reload || true
fi

install -d /etc/systemd/user
cat > /etc/systemd/user/mwb.service << 'EOF'
[Unit]
Description=Mouse Without Borders for Linux
After=graphical-session.target network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mwb
Restart=on-failure
RestartSec=5
# DISPLAY and XAUTHORITY are auto-detected by the mwb binary.

[Install]
WantedBy=default.target
EOF
echo "  Systemd user service installed."

echo ""
echo "=== Installation Complete ==="
echo ""
echo "Next steps:"
echo "  1. Edit ~/.config/mwb/config.toml with your Windows IP and security key"
echo "  2. Log out and back in (for group changes)"
echo "  3. Run receive-only: mwb"
echo "  4. Optional bidirectional mode: mwb -bidi -edge left"
echo "  5. Or enable receive-only autostart: systemctl --user enable --now mwb"
echo ""
