#!/usr/bin/env bash
# Downloads what the firecracker backend needs into DIR (default ./fc):
# the firecracker binary, a guest kernel, and an Ubuntu base image, all from
# the Firecracker project's releases and CI artifacts. x86_64 or aarch64.
set -euo pipefail
DIR=${1:-./fc}
ARCH=$(uname -m)
mkdir -p "$DIR"; cd "$DIR"
REL=$(curl -fsSL https://api.github.com/repos/firecracker-microvm/firecracker/releases/latest | grep -oP '"tag_name": "\K[^"]+')
curl -fsSL "https://github.com/firecracker-microvm/firecracker/releases/download/${REL}/firecracker-${REL}-${ARCH}.tgz" | tar xz
cp "release-${REL}-${ARCH}/firecracker-${REL}-${ARCH}" firecracker
rm -rf "release-${REL}-${ARCH}"
CI="firecracker-ci/v1.10/${ARCH}"
# newest key whose name matches exactly (the bucket also holds configs, keys, etc.)
latest() { curl -fsSL "http://spec.ccfc.min.s3.amazonaws.com/?prefix=${CI}/$1&list-type=2" | grep -oP "<Key>\K${CI}/$2(?=</Key>)" | sort -V | tail -1; }
KERNEL=$(latest vmlinux- 'vmlinux-[0-9]+\.[0-9]+\.[0-9]+')
IMAGE=$(latest ubuntu- 'ubuntu-[0-9]+\.[0-9]+\.squashfs')
[ -n "$KERNEL" ] && [ -n "$IMAGE" ] || { echo "could not find a kernel and an image under ${CI}" >&2; exit 1; }
curl -fsSL "https://s3.amazonaws.com/spec.ccfc.min/${KERNEL}" -o vmlinux
curl -fsSL "https://s3.amazonaws.com/spec.ccfc.min/${IMAGE}" -o base.squashfs
head -c 4 vmlinux | grep -q "ELF" || { echo "vmlinux is not an ELF kernel" >&2; exit 1; }
[ "$(head -c 4 base.squashfs)" = "hsqs" ] || { echo "base.squashfs is not a squashfs image" >&2; exit 1; }
echo "kernel ${KERNEL##*/}, image ${IMAGE##*/}"
echo "firecracker ${REL}, kernel, and base image in $(pwd)"
