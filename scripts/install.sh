#!/bin/bash
set -e

echo "=== MWB Linux Installer ==="
echo ""

# Check for root
if [ "$EUID" -ne 0 ]; then
    echo "Please run with sudo: sudo bash install.sh"
    exit 1
fi

ACTUAL_USER=${SUDO_USER:-$USER}

# Install system dependencies
echo "[1/6] Installing dependencies..."
apt-get update -qq
apt-get install -y -qq xdotool xinput xclip x11-xserver-utils > /dev/null 2>&1
echo "  Done."

# Setup uinput
echo "[2/6] Configuring uinput..."
modprobe uinput
echo 'uinput' > /etc/modules-load.d/uinput.conf
echo 'KERNEL=="uinput", GROUP="input", MODE="0660"' > /etc/udev/rules.d/99-uinput.rules
udevadm control --reload-rules
udevadm trigger /dev/uinput 2>/dev/null || true
echo "  Done."

# Add user to input group
echo "[3/6] Adding $ACTUAL_USER to input group..."
usermod -aG input "$ACTUAL_USER"
echo "  Done."

# Build
echo "[4/6] Building mwb..."
if command -v go &> /dev/null; then
    cd "$(dirname "$0")/.."
    go build -o mwb ./cmd/mwb/
    cp mwb /usr/local/bin/mwb
    chmod +x /usr/local/bin/mwb
    echo "  Installed to /usr/local/bin/mwb"
else
    echo "  Go not found. Please install Go 1.24+ and run 'make build && sudo make install'"
fi

# Create default config
echo "[5/6] Creating config template..."
CONFIG_DIR="/home/$ACTUAL_USER/.config/mwb"
mkdir -p "$CONFIG_DIR"
if [ ! -f "$CONFIG_DIR/config.toml" ]; then
    cat > "$CONFIG_DIR/config.toml" << 'EOF'
# MWB Linux Configuration
# Get the security key from PowerToys → Mouse Without Borders on Windows

host = "192.168.1.100"        # Windows machine IP address
key = "YourSecurityKey"       # Security key from PowerToys MWB
name = "linux"                # This machine's name (max 15 chars)
# port = 15100                # Base port (default 15100)
EOF
    chown "$ACTUAL_USER:$ACTUAL_USER" "$CONFIG_DIR/config.toml"
    echo "  Config template created at $CONFIG_DIR/config.toml"
    echo "  Edit it with your Windows machine's IP and security key."
else
    echo "  Config already exists at $CONFIG_DIR/config.toml"
fi

# Install systemd service
echo "[6/6] Installing systemd service..."
cat > /etc/systemd/user/mwb.service << 'EOF'
[Unit]
Description=Mouse Without Borders for Linux
After=graphical-session.target

[Service]
Type=simple
ExecStart=/usr/local/bin/mwb -bidi -edge left
Restart=on-failure
RestartSec=5
# DISPLAY and XAUTHORITY are auto-detected by the mwb binary.

[Install]
WantedBy=default.target
EOF
echo "  Systemd user service installed."
echo "  Enable with: systemctl --user enable --now mwb"

echo ""
echo "=== Installation Complete ==="
echo ""
echo "Next steps:"
echo "  1. Edit ~/.config/mwb/config.toml with your Windows IP and security key"
echo "  2. Log out and back in (for group changes)"
echo "  3. Run: mwb -bidi -edge left"
echo "  4. Or enable autostart: systemctl --user enable --now mwb"
echo ""
