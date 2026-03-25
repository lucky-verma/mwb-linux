#!/bin/sh
set -e

# Reload udev rules so the uinput rule takes effect
udevadm control --reload-rules || true
udevadm trigger || true

# Reload systemd so the user service is available
systemctl daemon-reload || true
