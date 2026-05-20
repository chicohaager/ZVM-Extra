#!/bin/sh
# Install the freshly built sysext on the current ZimaOS host.
# Run as root on the ZimaOS device itself, after build.sh succeeded.
set -eu

ROOT="$(cd "$(dirname "$0")" && pwd)"
RAW="$ROOT/dist/zima_vm_extras.raw"
NAME="zima_vm_extras"

[ "$(id -u)" -eq 0 ] || { echo "Run as root." >&2; exit 1; }
[ -f "$RAW" ] || { echo "Run ./build.sh first." >&2; exit 1; }

# Verify SHA256 if present.
if [ -f "$ROOT/dist/$NAME.raw.sha256" ]; then
  echo "Verifying SHA256..."
  ( cd "$ROOT/dist" && sha256sum -c "$NAME.raw.sha256" )
fi

DEST="/var/lib/extensions/$NAME.raw"
echo "Copying to $DEST..."
cp "$RAW" "$DEST"

echo "Merging sysext..."
systemd-sysext refresh

echo "Reloading systemd..."
systemctl daemon-reload

echo "Enabling service..."
systemctl enable --now zima-vm-extras.service || true

sleep 1
echo
echo "=== Status ==="
systemctl status zima-vm-extras.service --no-pager | head -15 || true
echo
echo "API: curl -s http://127.0.0.1:8473/api/health"
echo "UI:  http://$(hostname -I | awk '{print $1}')/modules/zima_vm_extras/"
