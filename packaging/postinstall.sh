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

# Add the person who ran the install to the input group, as scripts/install.sh
# does. Only an ordinary account that ran sudo (SUDO_USER) or a polkit prompt
# (PKEXEC_UID) is eligible. A root shell or a graphical software store gives no
# such user, so they get the manual instruction instead of a guess.
INSTALL_USER=""
if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
  INSTALL_USER="${SUDO_USER}"
elif [ -n "${PKEXEC_UID:-}" ] && [ "${PKEXEC_UID}" != "0" ]; then
  INSTALL_USER="$(getent passwd "${PKEXEC_UID}" | cut -d: -f1)"
fi

GROUP_STEP="  1. Add your user to the input group: sudo usermod -aG input \$USER
     Then log out and back in."
# The uinput udev rule above needs this group. Desktop systems already have it
# (systemd/udev create it); minimal ones may not.
getent group input >/dev/null 2>&1 || groupadd --system input 2>/dev/null || true
if [ -n "${INSTALL_USER}" ] && id "${INSTALL_USER}" >/dev/null 2>&1; then
  if id -nG "${INSTALL_USER}" | tr ' ' '\n' | grep -qx input; then
    GROUP_STEP="  1. ${INSTALL_USER} is already in the input group."
  elif usermod -aG input "${INSTALL_USER}" 2>/dev/null; then
    GROUP_STEP="  1. Added ${INSTALL_USER} to the input group. Log out and back in once."
  fi
fi

cat <<MSG

MWB Linux installed.

Next steps:
${GROUP_STEP}
  2. Open "Mouse Without Borders" from your apps (or run: mwb-gui),
     fill in the other computer and security key, then Save and Start.

  Prefer the terminal? Edit ~/.config/mwb/config.toml and run: mwb
  Bidirectional mode: mwb -bidi -edge left

MSG
