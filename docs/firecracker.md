# The firecracker backend

The process backend checkpoints files. The firecracker backend checkpoints a
running machine: files and memory, so a restore or a fork brings back a live
process at the exact instruction it was paused on, not a re-run from files.

This is the runtime vitvm was extracted from. It ran agent microVMs and
snapshotted them at every tool call. This document describes bringing that
backend up in the open; the process backend is what ships today.

## What it does

- Boots the sandbox as a Firecracker microVM from a root filesystem image.
- Runs each step inside the guest through a small guest agent.
- After a step, pauses the VM, captures the memory snapshot and the block
  device's changed blocks, and stores both as content-addressed blobs, the same
  store the process backend uses for files.
- A checkpoint therefore carries a tree (files) and a memory image (pages),
  both deduplicated. Unchanged memory pages across steps are stored once, the
  same way unchanged files are.

## Restore and fork

- **checkout** resumes the VM from a checkpoint's memory image and disk.
- **fork** boots a new VM from a checkpoint's memory and a copy-on-write clone
  of its disk, so two sandboxes diverge from a live point. This is how you
  branch a run at step 23 and try three different next steps from the same warm
  process.

## What a host needs

- Linux with KVM (`/dev/kvm`), so a bare cloud VM with nested virtualisation or
  a metal host.
- Firecracker, and a root filesystem image for the guest.
- A filesystem that supports reflinks (XFS or Btrfs) for cheap disk clones.

None of this is needed for the process backend, which is why that one ships
first and runs anywhere.

## Where the pieces are

The engine's `Backend` interface is the seam. The firecracker backend
implements `Exec` by driving the VM and, around it, hooks the checkpoint step
to capture memory in addition to the file tree. The store, the checkpoint
chain, and every `vit` command are already backend-agnostic.
