# vitvm

git for VM state. Every command saves the whole machine, memory and running
processes included, in 72 ms, and any step forks back to life in about 100 ms.
Read any past step, diff two steps, rewind, or fork a new machine that picks up
exactly where a step left off.

A fork doesn't replay anything. The processes that were running at that step
are still running.

Runs on any Linux host with KVM. Everywhere else, a files-only mode gives you
the same commands on a laptop.

The command is `vit`.

```
$ vit new agent
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

When a long run goes wrong at step 40, you rerun it to see what happened at
step 12. That's slow, and it isn't even the same run.

Saving files alone doesn't fix that, because what took the time is usually in
memory: a loaded model, a warm cache, an agent's state, a server that took a
minute to start. git already versions files. vitvm versions the machine they
live in.

So step 12 is still there, running. Fork it and try a different step 13. Fork
it ten times and try ten. Each fork starts warm in about 40 milliseconds.

## What a checkpoint is

Each step saves the machine as it was at one instant: memory, device state and
disk. It also saves the machine's files as a tree, so you can read or diff any
step without booting anything.

Nothing is copied whole. A step writes only the memory pages that changed and
the writable part of the disk, and everything is stored as chunks shared with
every other step.

## Speed

On an EC2 `c5.metal` with a 512 MiB guest:

| | time |
|---|---|
| a small step, including its full machine checkpoint | 72 ms |
| warm fork, first from a step | 109 ms |
| warm fork, again from the same step | 37 ms |
| first step of a new sandbox, including boot | 1.2 s |

- **Only changed memory is written.** Firecracker tracks the pages the guest
  touched since the last step, and only those are saved. If vitvm can't
  confirm which saved image a machine's memory came from, it saves all of it:
  slower, never wrong.
- **Only the writable disk is captured.** Every machine shares one read-only
  base disk, stored once, with a small writable layer on top.
- **Hashing is cheap.** It runs on every core and skips empty regions.
- **Forks share memory.** A step's memory is rebuilt once into a local cache,
  and every fork maps that one copy, copy-on-write.

Storage grows by what each step changed, about 25 MB per small step. Eleven
checkpoints that would be 16.9 GB stored whole took 575 MB.

## Machines and files only

Machine checkpoints need Linux with `/dev/kvm`: a bare-metal host, or a VM with
nested virtualisation. Everywhere else, vitvm runs each step as a local process
and saves just the files.

| | machine (firecracker) | files only (process) |
|---|---|---|
| where | Linux with `/dev/kvm` | any machine, no root |
| a step runs | inside the sandbox's microVM | as a local process |
| a checkpoint holds | memory, device state, disk, files | files |
| fork and checkout | resume the live machine | restore the files |
| `show`, `diff`, push, pull | yes | yes |

Files only is close to git with a commit after every command. It captures the
whole working directory without being asked, untracked files and large
binaries included, ties each snapshot to the command and exit code that made
it, and can fork any step. It's there so the same workflow runs on a laptop.

Once a Firecracker kernel and root filesystem are configured, new sandboxes are
machines. `--backend process` asks for files only. A fork keeps its source's
mode, and a checkpoint from either mode can be forked into the other.

A machine keeps running between `vit` commands. `vit stop` shuts it down, and
the next `vit run` brings it back from its latest step, warm.

## Install

```bash
go build -o /usr/local/bin/vit ./cmd/vit
```

No dependencies outside the Go standard library.

## Quick start

```bash
# firecracker, a guest kernel, and an Ubuntu base image
scripts/fetch-firecracker.sh /opt/fc
# a root filesystem with the vitvm agent as init (needs root)
sudo scripts/build-rootfs.sh /opt/fc/base.squashfs /opt/fc/rootfs.ext4

vit init
vit config firecracker.bin /opt/fc/firecracker
vit config firecracker.kernel /opt/fc/vmlinux
vit config firecracker.rootfs /opt/fc/rootfs.ext4

vit new agent
vit run -- sh -c 'nohup sh -c "i=0; while :; do i=\$((i+1)); echo \$i > /dev/shm/n; sleep 1; done" >/dev/null 2>&1 &'
vit run -- cat /dev/shm/n
vit fork "$(vit log | grep 'step 2' | grep -o 'ck-[0-9a-f]*')" retry
vit run -- cat /dev/shm/n        # the fork's counter carries on from step 2
```

`vit` needs access to `/dev/kvm`: root, or membership of the `kvm` group.

## Quick start, files only

```bash
cd "$(mktemp -d)"
vit init
vit new demo
vit run -- sh -c 'echo hello > note.txt'
vit run -- sh -c 'echo world >> note.txt'
vit log
vit show "$(vit log | grep 'step 1' | grep -o 'ck-[0-9a-f]*')" note.txt
```

## Commands

```
vit init                         create a repo (.vit) in the current directory
vit config [KEY [VALUE]]         show or set configuration
vit new [NAME] [--backend B]     start a sandbox (firecracker or process)
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

A repo lives in `.vit`, like `.git`; `VIT_DIR` points elsewhere. Any
configuration key can also come from the environment: `firecracker.kernel` is
`VIT_FIRECRACKER_KERNEL`. `vit config` lists them all. The ones worth knowing:

| key | meaning |
|---|---|
| `backend` | for new sandboxes: `firecracker` (the default once a kernel and rootfs are set) or `process` |
| `firecracker.mem_mib`, `firecracker.vcpus` | guest size (default 512 MiB, 1 vCPU) |
| `firecracker.disk_mode` | `overlay` (default: shared base plus a writable layer) or `copy` (a full private disk per machine, for guest kernels without overlayfs) |
| `firecracker.upper_gib` | size of the writable layer (default 8) |
| `gmux.remotes` | GPU hosts machines may use, `NAME=TOKEN@HOST:PORT#FP,...` (see below) |

Repeat forks are fast because of a local image cache in `.vit/cache`, capped by
`VIT_CACHE_GIB` (default 16).

## Remote checkpoint stores

Push a sandbox's history to any S3-compatible bucket and it outlives the
machine it ran on. Pull a checkpoint on another machine and fork it, warm if it
holds a machine image. Pushes are incremental: nothing already in the bucket is
sent twice.

```bash
export AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_REGION=us-east-1
vit push agent --to s3://my-bucket/checkpoints
vit pull ck-2c52a0bf6a14 --from s3://my-bucket/checkpoints   # on another machine
```

Works with AWS S3, MinIO, Cloudflare R2, Backblaze B2 and Ceph. Point
`AWS_ENDPOINT_URL` at the service, and set `VIT_S3_PATH_STYLE=1` where it needs
path-style addressing. A `dir:///path` remote works for a shared filesystem.
Details in [docs/remotes.md](docs/remotes.md).

## GPUs, with gmux

A vitvm machine has no GPU. Its sister project
[gmux](https://github.com/numinous-technology/gmux) ("tmux for GPUs") lends it
one: a step runs `gmux run`, the job runs on a gmux host's card under a share
and a memory cap, and its output files come back into the machine, where the
step's checkpoint keeps them.

```bash
# on the GPU host
gmux serve --addr :7070          # prints TOKEN@THIS-HOST:7070#FINGERPRINT

# on the vitvm host (the rootfs built with GMUX_BIN=/path/to/gmux)
vit config gmux.remotes "gpu1=TOKEN@GPU-HOST:7070#FINGERPRINT"
vit new train
vit run -- gmux run --share 0.25 --pull out.txt -- python bench.py out.txt
vit fork "$(vit log | grep 'step 1' | grep -o 'ck-[0-9a-f]*')" retry   # has out.txt, no GPU rerun
```

- **Nothing else is reachable.** A machine has no network device. For each
  configured host the guest gets a port on `127.0.0.1`, tunnelled over vsock
  to a forwarder next to the VM that connects to that one host. gmux's TLS
  runs end to end through it, pinned to the host's certificate.
- **Forks don't collide.** Each sandbox has its own gmux workspace on the
  host, so a fork's files never overwrite its parent's.
- **GPU time is accounted per sandbox and step.** Jobs run as owner
  `vit-SANDBOX` and are named `vit-SANDBOX-stepN`, so `gmux usage --by owner`
  shows what each branch of a run cost.
- **GPU memory is not checkpointed.** A fork resumes the machine, not the
  remote job. A step that leaves a `gmux run` going in the background says so
  as it is saved.

Tested between an EC2 `c5.metal` and a DigitalOcean MI350X: a quarter of the
card ran at 415.6 TFLOP/s from inside a machine, 416.7 from its fork, and all 9
checks passed. Transcript:
[docs/evidence/gmux-integration.txt](docs/evidence/gmux-integration.txt).

## Verified

The test suite runs on every build without a hypervisor: the engine against a
fake machine, and the Firecracker driver against a stand-in `firecracker` that
serves the real guest agent protocol.

On an EC2 `c5.metal` with Firecracker 1.17, the whole flow above ran through
`vit` and passed all 17 checks. Commands run in the guest kernel. A background
process survives between steps. `show` and `diff` read machine checkpoints. A
fork resumes the step's running process and its disk, not a later one.
Checkout rewinds the machine, and stop then run resumes it. A pushed
checkpoint pulls and forks warm in another repo. Images dedupe, and the speed
figures above hold. Transcript:
[docs/evidence/firecracker-cli-e2e.txt](docs/evidence/firecracker-cli-e2e.txt).

## How it works

- [docs/how-it-works.md](docs/how-it-works.md): the store, trees, the
  checkpoint chain, and what each command does.
- [docs/firecracker.md](docs/firecracker.md): the machine backend, the guest
  agent, and what a host needs.
- [docs/remotes.md](docs/remotes.md): pushing and pulling checkpoints.

## License

Apache-2.0.
