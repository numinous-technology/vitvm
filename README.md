# vitvm

git for virtual machines. A sandbox that checkpoints at every step, so you can
log its history, read any past step, diff two steps, rewind, and fork a new
sandbox from any point. On a KVM host a fork resumes the live machine, running
processes and all.

The command is `vit`.

```
$ vit new agent --backend firecracker
$ vit run -- python agent.py --task fix-the-bug
...
$ vit log
* ck-5c91  step 3   exit 0   ~1 +machine  python agent.py --step 3
  ck-5b30  step 2   exit 0   ~1 +machine  python agent.py --step 2
  ck-44c4  step 1   exit 0   +4 +machine  python agent.py --step 1
  ck-04e2  step 0            (created)
$ vit show ck-5b30 solution.py      # a file as it was at step 2, no machine needed
$ vit diff ck-5b30 ck-5c91          # what step 3 changed
$ vit fork ck-5b30 retry            # a new machine, resumed exactly at step 2
```

## Why

When a long agent run or a build goes wrong at step 40, you usually rerun the
whole thing to see what happened at step 12. That is slow and it is not even
the same run. vitvm keeps every step, so step 12 is still there: its files, its
diff, and a fork you can restart from.

It is version control applied to a running sandbox instead of a source tree.
Each command leaves a checkpoint, and checkpoints share unchanged data, so
keeping all of them is cheap.

## What a checkpoint is

Every checkpoint holds the sandbox's files: a tree of content-addressed blobs,
so a file that never changes across a hundred steps is stored once. Reading or
diffing a checkpoint is served from the store and never touches a machine.

On the firecracker backend a checkpoint also holds the machine: its memory, its
device state and its disk, captured at the same instant. Only what changed is
written, and everything is stored as content-addressed chunks shared with
earlier steps (see Speed below).

## Speed

Checkpointing a whole machine at every step is only useful if it is cheap. On
an EC2 `c5.metal` with a 512 MiB guest:

| | time |
|---|---|
| a small step, including its full machine checkpoint | 72 ms |
| warm fork, first from a step | 109 ms |
| warm fork, again from the same step | 37 ms |
| first step of a new sandbox, including boot | 1.2 s |

Four things make that possible:

- **Only changed memory is written.** Firecracker tracks the pages the guest
  dirtied since the last step, and only those are saved; the rest of the image
  reuses the previous step's chunks without being read. If vitvm cannot
  confirm which saved image a machine's memory came from, it saves all of
  memory instead, so a mismatch costs time, never correctness.
- **Only the writable disk is captured.** Every machine's root filesystem is
  the same read-only base, stored once, with a small sparse writable disk
  layered over it inside the guest. A step captures just that layer.
- **Hashing is cheap.** Images are hashed on every core, and empty regions of
  sparse files are never read.
- **Forks share memory.** A step's memory image is rebuilt once into a local
  cache, and every fork maps that one file copy-on-write instead of copying it.

Storage grows by what each step changed, about 25 MB per small step here: 11
checkpoints that would be 16.9 GB stored whole took 575 MB.

## Backends

| | process | firecracker |
|---|---|---|
| where | any machine, no root | Linux with `/dev/kvm` |
| a step runs | as a local process | inside the sandbox's microVM |
| a checkpoint holds | files | files, memory, device state, disk |
| fork and checkout | restore the files | resume the live machine |
| `show`, `diff`, push, pull | yes | yes |

Choose per sandbox with `vit new --backend`, or set a default with
`vit config backend firecracker`. A fork uses its source's backend unless told
otherwise, and a checkpoint from either backend can be forked on the other:
without a memory image, a firecracker fork boots a fresh machine and writes the
checkpoint's files into it.

A firecracker machine keeps running between `vit` commands. `vit stop` shuts it
down; the next `vit run` brings it back from its latest checkpoint, warm.

## Install

```bash
go build -o /usr/local/bin/vit ./cmd/vit
```

vitvm has no dependencies outside the Go standard library.

## Quick start: files, anywhere

```bash
cd "$(mktemp -d)"
vit init
vit new demo
vit run -- sh -c 'echo hello > note.txt'
vit run -- sh -c 'echo world >> note.txt'
vit log
vit show "$(vit log | grep 'step 1' | grep -o 'ck-[0-9a-f]*')" note.txt
```

## Quick start: live machines on KVM

```bash
# firecracker, a guest kernel, and an Ubuntu base image
scripts/fetch-firecracker.sh /opt/fc
# a root filesystem with the vitvm agent as init (needs root)
sudo scripts/build-rootfs.sh /opt/fc/base.squashfs /opt/fc/rootfs.ext4

vit init
vit config backend firecracker
vit config firecracker.bin /opt/fc/firecracker
vit config firecracker.kernel /opt/fc/vmlinux
vit config firecracker.rootfs /opt/fc/rootfs.ext4

vit new agent
vit run -- sh -c 'nohup sh -c "i=0; while :; do i=\$((i+1)); echo \$i > /dev/shm/n; sleep 1; done" >/dev/null 2>&1 &'
vit run -- cat /dev/shm/n
vit fork "$(vit log | grep 'step 2' | grep -o 'ck-[0-9a-f]*')" retry
vit run -- cat /dev/shm/n        # the fork's counter carries on from step 2
```

`vit` needs access to `/dev/kvm` (root, or membership of the `kvm` group).

## Commands

```
vit init                         create a repo (.vit) in the current directory
vit config [KEY [VALUE]]         show or set configuration
vit new [NAME] [--backend B]     start a sandbox (process or firecracker)
vit run -- CMD...                run a command; the result is a checkpoint
vit log [SANDBOX]                the checkpoints of a sandbox, newest first
vit ls                           list sandboxes and whether each machine is up
vit status                       the current sandbox and its head
vit use SANDBOX                  make a sandbox current
vit checkout CHECKPOINT          rewind the current sandbox to a checkpoint
vit diff CK [CK2]                what changed at a checkpoint, or between two
vit show CHECKPOINT PATH         print a file as it was at a checkpoint
vit fork CHECKPOINT [NAME]       branch a new sandbox from any checkpoint
vit stop [SANDBOX]               shut a sandbox's machine down (history kept)
vit push [SANDBOX] --to URL      upload a sandbox's checkpoints to a remote
vit pull CHECKPOINT --from URL   pull a checkpoint and fork it here
```

A repo lives in `.vit` in the current directory, like `.git`; set `VIT_DIR` to
point elsewhere. Every configuration key can also be set in the environment:
`firecracker.kernel` is `VIT_FIRECRACKER_KERNEL`. `vit config` lists them all;
the ones worth knowing:

| key | meaning |
|---|---|
| `backend` | `process` (default) or `firecracker` for new sandboxes |
| `firecracker.mem_mib`, `firecracker.vcpus` | guest size (default 512 MiB, 1 vCPU) |
| `firecracker.disk_mode` | `overlay` (default: shared base + writable layer) or `copy` (a full private disk per machine, for guest kernels without overlayfs) |
| `firecracker.upper_gib` | size of the sparse writable disk (default 8) |

The local image cache that makes repeat forks fast lives in `.vit/cache` and is
capped by `VIT_CACHE_GIB` (default 16).

## Remote checkpoint stores

Checkpoints push to any S3-compatible object store, so a sandbox's history
outlives the machine it ran on and another machine can pull a checkpoint and
fork from it, warm if it carries a machine image. Pushing is incremental:
blobs and image chunks already in the bucket are never uploaded twice.

```bash
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_REGION=us-east-1
vit push agent --to s3://my-bucket/checkpoints
vit pull ck-2c52a0bf6a14 --from s3://my-bucket/checkpoints   # on another machine
```

It works with AWS S3, MinIO, Cloudflare R2, Backblaze B2 and Ceph; point
`AWS_ENDPOINT_URL` at the service and set `VIT_S3_PATH_STYLE=1` where it needs
path-style addressing. A `dir:///path` remote works for a shared filesystem.
Details in [docs/remotes.md](docs/remotes.md).

## Verified

The full test suite runs on every build without a hypervisor. The engine's
machine path runs against an in-process fake machine, and the Firecracker
driver runs against a stand-in `firecracker` binary that serves the real guest
agent protocol.

On an EC2 `c5.metal` host, the complete flow above ran through the `vit`
command with Firecracker 1.17 and passed all 17 checks: commands run in the
guest kernel; a background process survives between steps; `show` and `diff`
read machine checkpoints; a fork resumes the step's running process and its
disk, not a later one; checkout rewinds the machine; stop and run resume it; a
pushed checkpoint pulls and forks warm in another repo; chunked images dedupe;
and the speed figures above. Transcript: [docs/evidence/firecracker-cli-e2e.txt](docs/evidence/firecracker-cli-e2e.txt).

## How it works

- [docs/how-it-works.md](docs/how-it-works.md): the store, trees, the
  checkpoint chain, and what each command does.
- [docs/firecracker.md](docs/firecracker.md): the machine backend, the guest
  agent, and what a host needs.
- [docs/remotes.md](docs/remotes.md): pushing and pulling checkpoints.

## License

Apache-2.0.
