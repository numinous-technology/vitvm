# The Firecracker backend

The process backend checkpoints files. The Firecracker backend checkpoints a
running machine: each sandbox is a Firecracker microVM, and each step records
the machine's memory, device state and root disk at one instant along with its
files. A checkout or a fork resumes the machine exactly where that step left
it, with its processes still running.

## A step

1. If the sandbox's machine is not running, it is brought back from the head
   checkpoint: resumed from its image, or, for a checkpoint without one, booted
   fresh with the checkpoint's files written in.
2. The command runs inside the guest through the vitvm agent.
3. The engine reads the guest's `/work` tree through the agent. Files carry a
   hash computed in the guest, so only contents the store lacks cross over.
4. The machine is paused, Firecracker writes a diff snapshot (only the memory
   pages dirtied since the last step) and the device state, the writable disk
   is copied while still paused, and the machine resumes.
5. The diff is laid over the previous step's memory image: chunks it does not
   touch keep their hashes without being read, so memory costs what changed.
   The writable disk is chunked the same way, skipping its holes. The
   checkpoint records the tree, the memory, disk and base disk manifests, and
   the device state.

Because the disk is captured at the same instant as memory, a resumed machine
never sees a filesystem that moved on without it. A diff is taken only when
the driver can confirm which stored image the VM's memory derives from (it
records that in the VM's directory on every resume and snapshot); otherwise
the snapshot is full, so a mismatch costs time and never correctness.

## Disks

By default the root filesystem is a shared read-only base, the same file for
every VM, with a sparse 8 GiB writable disk layered over it inside the guest
(overlayfs, then `pivot_root`). The base is stored in the repo once per
distinct file; only the writable disk is captured per step, and it holds just
what the sandbox wrote. `vit config firecracker.disk_mode copy` gives each VM a
private copy of the whole root filesystem instead, for guest kernels without
overlayfs.

## Fork and checkout

A checkpoint with an image resumes warm. Its memory, state and disks are
reassembled once into the repo's image cache (`.vit/cache`, bounded by
`VIT_CACHE_GIB`, default 16). A new Firecracker in the target sandbox's own
directory maps the cached memory copy-on-write, so any number of forks of one
step share a single file, gets its own copy of the small writable disk, and
carries on. Two forks of one step are two independent machines from the same
instant.

Each VM runs in its own directory, and the disk and vsock paths given to
Firecracker are relative (`rootfs.ext4`, `v.sock`). A snapshot records those
names, so a fork loaded in its own directory opens its own disk and socket, and
never the original's.

## Machines between commands

Firecracker runs detached from `vit`, so a machine outlives the command that
started it, and the driver keeps no state of its own: every `vit` command finds
a sandbox's machine from its directory under `firecracker.run_dir` (default
`/tmp/vit-fc`).

```
/tmp/vit-fc/<sandbox>/api.sock     Firecracker's API socket
/tmp/vit-fc/<sandbox>/v.sock       vsock socket to the guest agent
/tmp/vit-fc/<sandbox>/base.ext4    link to the shared read-only base
/tmp/vit-fc/<sandbox>/upper.ext4   this VM's writable disk (sparse)
/tmp/vit-fc/<sandbox>/pid, fc.log  process id; console and Firecracker log
```

`vit stop` kills the machine and removes its directory; everything that matters
is in its checkpoints, and the next `vit run` resumes the head checkpoint.

## The guest agent

`cmd/vit-guest` is the guest's init. As PID 1 it mounts `/proc`, `/sys`, `/dev`,
`/dev/shm`, `/dev/pts` and `/run`, starts itself again as the agent, and reaps
every orphaned process, so background processes a sandbox starts do not pile
up. The agent listens on vsock port 1024 and serves one request per connection
(`internal/agent`): `exec`, `tree`, `read`, `write`, `clear` and `ping`.

## Setting up a host

- Linux with KVM (`/dev/kvm`): a bare-metal instance or a VM with nested
  virtualisation. `vit` needs access to `/dev/kvm`.
- `scripts/fetch-firecracker.sh DIR` downloads the Firecracker binary, a guest
  kernel and an Ubuntu base image from the Firecracker project.
- `sudo scripts/build-rootfs.sh BASE OUT [SIZE_MB]` installs the agent as init
  into a base image and writes an ext4 root filesystem.
- Point vit at them:

```bash
vit config firecracker.bin    /opt/fc/firecracker
vit config firecracker.kernel /opt/fc/vmlinux
vit config firecracker.rootfs /opt/fc/rootfs.ext4
vit config firecracker.mem_mib 512      # optional, default 512
vit config firecracker.vcpus 2          # optional, default 1
```

On a filesystem with reflinks (XFS, Btrfs), copying a disk image shares blocks
and is nearly free; elsewhere it is a sparse copy.

## Cost of a step

On an EC2 `c5.metal` with a 512 MiB guest, a small step including its full
machine checkpoint takes 72 ms; a warm fork takes 109 ms the first time from a
step and 37 ms after that, from the cache; the first step of a new sandbox,
including boot, takes 1.2 s. The same steps in copy disk mode take 266 ms.
Storage grows by the chunks a step changed, about 25 MB per small step here:
11 checkpoints that would be 16.9 GB stored whole took 575 MB. Transcript:
[evidence/firecracker-cli-e2e.txt](evidence/firecracker-cli-e2e.txt).

## Testing without KVM

`internal/fcvm` is tested against `testdata/fakefc`, a stand-in `firecracker`
that serves the same REST calls from the VM's directory and runs the real guest
agent behind Firecracker's vsock handshake, keeping the guest's files inside its
"disk" and a boot nonce in its "memory". The tests cover the boot sequence and
relative device paths, a VM found again by a new driver, guest files, snapshot
order, fork isolation, shutdown, and the engine end to end through the driver.
