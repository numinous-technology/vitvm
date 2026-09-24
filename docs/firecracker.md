# The Firecracker backend

The process backend checkpoints files. The Firecracker backend checkpoints a
running machine: it boots a sandbox as a Firecracker microVM and snapshots the
guest's memory and device state, so a restore or a fork resumes a live process
at the point it was paused rather than replaying from files.

It implements `engine.MemoryBackend` and speaks Firecracker's REST API over
each VM's unix socket using only the standard library.

## What it does

- **Boot** launches a firecracker process, configures the boot source, root
  drive, machine size and a vsock device, and starts the guest.
- **Exec** runs a command inside the guest through a small in-guest agent
  (`cmd/vit-guest`) over vsock, and streams the output back.
- **Snapshot** pauses the VM, writes a full snapshot (a memory image plus the
  device state), and resumes it. The engine stores both as content-addressed
  blobs, so unchanged memory pages across steps are stored once, the same way
  unchanged files are.
- **Restore** resumes the sandbox from a snapshot. **Fork** resumes a copy of a
  snapshot as a new sandbox, so two machines diverge from the same live point.

## Verified on real hardware

The backend was run on a Linux KVM host (an EC2 `c5.metal`) with Firecracker
v1.17 and a 6.1 guest kernel. The transcript is in
[evidence/firecracker-real-kvm.txt](evidence/firecracker-real-kvm.txt):

- A microVM boots and runs commands in the guest (`uname -r` returns the guest
  kernel `6.1.102`, not the host's, and a file written in the guest reads back).
- A 256 MiB memory snapshot is taken, then forked twice. Both forks resume the
  original's in-RAM nonce and continue its counter from the snapshot point. A
  cold boot generates a new random nonce, so a matching nonce across forks is
  proof the live memory was restored, not rebooted.

Two layers of tests cover it without a hypervisor:

- `internal/fcvm` tests the driver's exact REST sequence (boot, the
  pause/create/resume order of a snapshot, and load-and-resume on a fork)
  against a stand-in firecracker binary.
- `internal/engine` tests the memory-checkpoint chain (capture at each step,
  restore on checkout, fork resuming the right step's memory) against a fake
  memory backend using real snapshot files.

## What a host needs

- Linux with KVM (`/dev/kvm`): a bare-metal instance or a nested-virtualisation
  VM.
- The `firecracker` binary, an uncompressed guest kernel, and a root filesystem
  image that runs the in-guest agent.
- A filesystem that supports reflinks (XFS or Btrfs) for cheap disk clones when
  forking disk state.

None of this is needed for the process backend, which checkpoints files and
runs on any machine.

## Remaining integration

The backend snapshots the guest's memory and its own root filesystem. Unifying
that with the engine's file tree through `vit run`, so a single checkpoint holds
both the host-visible working tree and the guest memory, needs the working
directory shared into the guest (virtio-fs or a shared drive). Until then, the
Firecracker backend checkpoints the machine (memory and its disk) and the
process backend checkpoints the working tree; both use the same content-
addressed store and the same `vit` commands.
