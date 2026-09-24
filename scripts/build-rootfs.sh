#!/usr/bin/env bash
# Builds a root filesystem for vitvm's firecracker backend.
#
#   scripts/build-rootfs.sh BASE OUT [SIZE_MB]
#
# BASE is a squashfs image, an ext4 image, or a directory holding a Linux
# userland (scripts/fetch-firecracker.sh downloads an Ubuntu one). The script
# installs the vitvm guest agent as the VM's init and writes an ext4 image to
# OUT. Needs root (for loop mounts and to keep file ownership), and the agent
# binary at $VIT_GUEST (default: build it from this repo).
set -euo pipefail
BASE=$1; OUT=$2; SIZE=${3:-1024}
HERE=$(cd "$(dirname "$0")/.." && pwd)
AGENT=${VIT_GUEST:-}
if [ -z "$AGENT" ]; then
  AGENT=$(mktemp)
  (cd "$HERE" && CGO_ENABLED=0 go build -o "$AGENT" ./cmd/vit-guest)
fi
ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
case "$BASE" in
  *.squashfs) unsquashfs -f -d "$ROOT" "$BASE" >/dev/null ;;
  *.ext4|*.img) M=$(mktemp -d); mount -o loop,ro "$BASE" "$M"; cp -a "$M/." "$ROOT/"; umount "$M"; rmdir "$M" ;;
  *) cp -a "$BASE/." "$ROOT/" ;;
esac
install -m 0755 "$AGENT" "$ROOT/usr/bin/vit-guest"
ln -sf /usr/bin/vit-guest "$ROOT/sbin/vit-init"
mkdir -p "$ROOT/work" "$ROOT/mnt"  # /mnt: scratch space for the overlay root
rm -f "$OUT"
truncate -s "${SIZE}M" "$OUT"
mkfs.ext4 -q -F -d "$ROOT" "$OUT"
echo "built $OUT (${SIZE} MiB) with the vitvm agent as init"
