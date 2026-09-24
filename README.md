# vitvm

git for virtual machines. A sandbox that checkpoints at every step, so you can
log its history, read any past step, diff two steps, rewind, and fork a new
sandbox from any point.

The command is `vit`.

```
$ vit new agent
$ vit run -- python agent.py --task fix-the-bug
...
$ vit log
* ck-5c91  step 3   exit 0   ~1  python agent.py --step 3
  ck-5b30  step 2   exit 0   ~1  python agent.py --step 2
  ck-44c4  step 1   exit 0   +4  python agent.py --step 1
  ck-04e2  step 0            (created)
$ vit show ck-5b30 solution.py      # read a file as it was at step 2
$ vit diff ck-5b30 ck-5c91          # what step 3 changed
$ vit fork ck-5b30 retry            # branch a fresh sandbox from step 2
```

## Why

When a long agent run or a build goes wrong at step 40, you usually rerun the
whole thing to see what happened at step 12. That is slow and it is not even
the same run. vitvm keeps every step, so step 12 is still there: its files, its
diff, and a fork you can restart from.

It is the same idea as version control, applied to a running sandbox instead of
a source tree. Each command leaves a checkpoint. Checkpoints share unchanged
data, so keeping all of them is cheap.

## What a checkpoint is

A checkpoint is a content-addressed snapshot of the sandbox plus a pointer to
its parent. Every file is stored once by its hash, so a file that does not
change across a hundred steps costs one copy. Reading, diffing or forking a
checkpoint never touches the running sandbox and never re-runs anything: it is
all served from the store.

## Backends

vitvm has a backend interface. What a checkpoint captures depends on it.

- **process** (shipping): runs each step as a local process and checkpoints the
  sandbox's files afterward. Works on any machine, no hypervisor, no root. This
  is what the examples use.
- **firecracker** (design in [docs/firecracker.md](docs/firecracker.md)): runs
  the sandbox inside a Firecracker microVM and adds the guest's memory to each
  checkpoint, so a restore or a fork brings back a live process, not just files.
  This is the runtime vitvm was extracted from; the open backend is being
  brought up on a KVM host.

The store, the checkpoint chain, and every `vit` command are the same across
backends. Files are always captured; memory is captured when the backend can.

## Install

```bash
go build -o vit ./cmd/vit
```

## Commands

```
vit init                    create a repo (.vit) in the current directory
vit new [name]              start a sandbox and make it current
vit run -- CMD...           run a command; the result is a checkpoint
vit log [sandbox]           the checkpoints of a sandbox, newest first
vit ls                      list sandboxes
vit status                  the current sandbox and where its head is
vit checkout CHECKPOINT     rewind the current sandbox to a checkpoint
vit diff CK [CK2]           what changed at a checkpoint, or between two
vit show CHECKPOINT PATH    print a file as it was at a checkpoint
vit fork CHECKPOINT [name]  branch a new sandbox from any checkpoint
```

A repo lives in `.vit` in the current directory (like `.git`). Set `VIT_DIR`
to point elsewhere.

## Try it

```bash
go build -o /usr/local/bin/vit ./cmd/vit
cd $(mktemp -d)
vit init
vit new demo
vit run -- sh -c 'echo hello > note.txt'
vit run -- sh -c 'echo world >> note.txt'
vit log
vit show "$(vit log | grep 'step 1' | grep -o 'ck-[0-9a-f]*')" note.txt
```

There is a fuller walkthrough in [examples/fork-from-step](examples/fork-from-step).

## How it works

- [docs/how-it-works.md](docs/how-it-works.md): the store, trees, the
  checkpoint chain, and what each command does.
- [docs/firecracker.md](docs/firecracker.md): the memory-checkpoint backend and
  what a KVM host needs to run it.

## Status

Early and honest about it. The process backend, the content-addressed store,
the checkpoint chain, and run, log, checkout, diff, show and fork all work and
are tested. The firecracker backend that adds live memory is documented and
being brought up; its interface is in the tree. vitvm grew out of a production
runtime that checkpointed agent microVMs at every tool call; this is that idea,
rebuilt in the open, files first.

## License

Apache-2.0.
