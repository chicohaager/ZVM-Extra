#!/bin/sh
# Build a zima_vm_extras.raw sysext from this repo.
# Requirements on the build host:
#   - go 1.22+
#   - squashfs-tools (mksquashfs)
# Output: dist/zima_vm_extras.raw + dist/zima_vm_extras.raw.sha256
set -eu

ROOT="$(cd "$(dirname "$0")" && pwd)"
DIST="$ROOT/dist"
RAW="$ROOT/raw"
NAME="zima_vm_extras"
VERSION="${VERSION:-$(cat "$ROOT/VERSION" 2>/dev/null || echo dev)}"

echo "=== zima-vm-extras build ==="
echo "Working dir: $ROOT"
echo "Version:     $VERSION"

# 1. Vet, test, then compile the Go daemon (cgo-free, static).
echo "[1/3] go vet + test + build..."
mkdir -p "$RAW/usr/bin"
cd "$ROOT"
go vet ./...
go test ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build \
    -trimpath \
    -ldflags="-s -w -X github.com/chicohaager/zima-vm-extras/internal/buildinfo.Version=$VERSION" \
    -o "$RAW/usr/bin/zima-vm-extras" \
    ./cmd/zima-vm-extras
chmod +x "$RAW/usr/bin/zima-vm-extras"
echo "  → $(ls -lh "$RAW/usr/bin/zima-vm-extras" | awk '{print $5}')"

# 2. Verify the layout matches the sysext / CasaOS conventions.
echo "[2/3] Verifying layout..."
required="
$RAW/usr/lib/extension-release.d/extension-release.$NAME
$RAW/usr/lib/systemd/system/zima-vm-extras.service
$RAW/usr/share/casaos/modules/$NAME.json
$RAW/usr/share/casaos/www/modules/$NAME/index.html
$RAW/usr/share/casaos/www/modules/$NAME/app.js
$RAW/usr/share/casaos/www/modules/$NAME/appicon.svg
$RAW/usr/bin/zima-vm-extras
"
missing=0
for f in $required; do
  if [ ! -e "$f" ]; then
    echo "  MISSING: $f"
    missing=1
  fi
done
[ $missing -eq 0 ] || { echo "Layout incomplete, aborting."; exit 1; }

# 3. Pack as squashfs.
echo "[3/3] Packing squashfs..."
mkdir -p "$DIST"
rm -f "$DIST/$NAME.raw" "$DIST/$NAME.raw.sha256"
# NOTE: ZimaOS 1.6.x kernel (6.12.25) is built without CONFIG_SQUASHFS_XZ.
# Mounting an xz-compressed sysext fails with "Filesystem uses 'xz' compression.
# This is not supported." Use gzip — supported on every standard kernel build.
mksquashfs "$RAW" "$DIST/$NAME.raw" \
  -all-root \
  -comp gzip \
  -noappend \
  -no-progress \
  >/dev/null

# 4. Compute SHA256 next to the artifact so verification is trivial.
( cd "$DIST" && sha256sum "$NAME.raw" > "$NAME.raw.sha256" )

echo
echo "=== Done ==="
ls -lh "$DIST/"
echo
# ZimaOS 1.7.0 ships sshd with PermitRootLogin no, so the install path goes
# through the admin account and sudo — copying straight to root@<host> fails
# at the login, not at the copy.
echo "Install on ZimaOS (admin account + sudo; root SSH login is disabled):"
echo "  scp $DIST/$NAME.raw <user>@<host>:/tmp/"
echo "  ssh <user>@<host> 'sudo install -m 644 /tmp/$NAME.raw /var/lib/extensions/ && \\"
echo "    sudo systemd-sysext refresh && sudo systemctl daemon-reload && \\"
echo "    sudo systemctl enable --now zima-vm-extras.service'"
