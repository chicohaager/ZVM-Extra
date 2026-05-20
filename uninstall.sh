#!/bin/sh
# Remove zima-vm-extras cleanly from a ZimaOS host.
set -u
[ "$(id -u)" -eq 0 ] || { echo "Run as root." >&2; exit 1; }

# Stop and disable the main service.
systemctl disable --now zima-vm-extras.service 2>/dev/null || true

# Remove the boot watchdog (timer + service on the persistent root).
systemctl disable --now zima-vm-extras-watchdog.timer 2>/dev/null || true
rm -f /etc/systemd/system/zima-vm-extras-watchdog.service \
      /etc/systemd/system/zima-vm-extras-watchdog.timer

# Remove the sysext image and re-merge.
rm -f /var/lib/extensions/zima_vm_extras.raw
systemd-sysext refresh || true
systemctl daemon-reload

# Remove the gateway route (best effort — the gateway has no DELETE API,
# so a stale route would otherwise linger and return 502).
MGMT="$(cat /var/run/casaos/management.url 2>/dev/null)"
[ -n "$MGMT" ] && curl -s -X DELETE "$MGMT/v1/gateway/routes/v2/vm_extras" >/dev/null 2>&1 || true

echo "Removed. State under /DATA/AppData/zima-vm-extras/ is preserved."
echo "To wipe state too: rm -rf /DATA/AppData/zima-vm-extras/"
