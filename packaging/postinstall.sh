#!/bin/bash
set -e

modprobe uinput 2>/dev/null || true
echo 'uinput' > /etc/modules-load.d/uinput.conf 2>/dev/null || true
udevadm control --reload-rules 2>/dev/null || true
udevadm trigger /dev/uinput 2>/dev/null || true

if command -v systemctl >/dev/null 2>&1 && \
  systemctl list-unit-files --no-legend mwb-linux.service 2>/dev/null | grep -q '^mwb-linux\.service'; then
  systemctl disable --now mwb-linux.service >/dev/null 2>&1 || true
  rm -f /etc/systemd/system/mwb-linux.service
  rm -rf /etc/systemd/system/mwb-linux.service.d
  systemctl daemon-reload || true
fi

cat <<'MSG'

MWB Linux installed.

Next steps:
  1. Add your user to the input group: sudo usermod -aG input $USER
  2. Create config: mkdir -p ~/.config/mwb && nano ~/.config/mwb/config.toml
  3. Run receive-only: mwb
  4. Optional bidirectional mode: mwb -bidi -edge left

MSG
