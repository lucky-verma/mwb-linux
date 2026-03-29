#!/bin/sh
# Best-effort stop of the user service.
# This runs as root during package removal, so we can't reliably stop
# a user-level systemd service. We try common approaches.

# Try to stop for all logged-in users
for uid in $(loginctl list-users --no-legend 2>/dev/null | awk '{print $1}'); do
    systemctl --user --machine="${uid}@" stop mwb.service 2>/dev/null || true
done

exit 0
