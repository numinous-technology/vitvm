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
4. The machine is paused, Firecracker writes a full snapshot (memory and device
   state), the disk is copied while still paused, and the machine resumes.
5. Memory and disk are stored as 1 MiB content-addressed chunks with a small
   manifest each; the device state is a plain blob. The checkpoint records the
   tree and the three image hashes.

Because the disk is captured at the same instant as memory, a resumed machine
never sees a filesystem that moved on without it.

## Fork and checkout

A checkpoint with an image resumes warm: the disk, memory and state are
reassembled from their chunks into the target sandbox's own directory, a new
Firecracker loads the snapshot there, and the guest carries on. Two forks of
one step are two independent machines from the same instant.

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
/tmp/vit-fc/<sandbox>/rootfs.ext4  this VM's disk
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

On an EC2 `c5.metal` with a 512 MiB guest and a 1 GiB disk, a step takes about
5 seconds, most of it writing and hashing the memory snapshot and the disk
copy, and a warm fork about 1.7 seconds. Storage grows only by the chunks a
step changed: 11 checkpoints that would be 16.9 GB stored whole took 532 MB.
The end-to-end transcript is in
[evidence/firecracker-cli-e2e.txt](evidence/firecracker-cli-e2e.txt).

## Testing without KVM

`internal/fcvm` is tested against `testdata/fakefc`, a stand-in `firecracker`
that serves the same REST calls from the VM's directory and runs the real guest
agent behind Firecracker's vsock handshake, keeping the guest's files inside its
"disk" and a boot nonce in its "memory". The tests cover the boot sequence and
relative device paths, a VM found again by a new driver, guest files, snapshot
order, fork isolation, shutdown, and the engine end to end through the driver.
