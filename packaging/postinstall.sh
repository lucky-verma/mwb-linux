#!/bin/bash
set -e

modprobe uinput 2>/dev/null || true
echo 'uinput' > /etc/modules-load.d/uinput.conf 2>/dev/null || true
udevadm control --reload-rules 2>/dev/null || true
udevadm trigger /dev/uinput 2>/dev/null || true

cat <<'MSG'

MWB Linux installed.

Next steps:
  1. Add your user to the input group: sudo usermod -aG input $USER
  2. Create config: mkdir -p ~/.config/mwb && nano ~/.config/mwb/config.toml
  3. Run: mwb -bidi -edge left

MSG
