# How vitvm works

Four small pieces: a blob store, a tree, a checkpoint chain, and an engine that
runs commands and snapshots them.

## The blob store (`internal/cas`)

Content-addressed: you hand it bytes, it returns their sha256 and stores them
once. Two files with the same contents, anywhere in any sandbox at any step,
are one object on disk. Large files are streamed, not held in memory.

## Trees (`internal/tree`)

A tree is a sorted list of a directory's entries: for each path, its mode,
size, and the hash of its contents (or its symlink target). Snapshotting a
directory walks it, stores each file in the blob store, and records the tree.
The tree itself is JSON and is stored as a blob, so it too is deduplicated and
addressed by hash.

Two trees can be diffed into added, modified and deleted paths. That is what
`vit diff` shows and how each checkpoint counts its changes.

Restoring (`internal/snap`) is the inverse: given a tree, lay the exact set of
files down in a directory, removing anything that is not in the tree. That is
what `vit checkout` and `vit fork` use.

## The checkpoint chain (`internal/engine`)

A checkpoint records the sandbox it belongs to, its step number, its parent
checkpoint, the hash of its tree, the command that produced it, the exit code,
and the change counts. A sandbox points at its head checkpoint. Walking parents
from the head gives the history.

Because a checkpoint is just a tree hash plus a parent pointer, and trees and
files are shared blobs, keeping every step is cheap: a step that changes one
file adds one small blob and one small tree.

## The engine

- **run**: execute the command in the sandbox's working directory through the
  backend, then snapshot the directory into a new checkpoint on top of the
  head. Every step leaves a checkpoint.
- **show / read**: resolve a path in a checkpoint's tree to a blob and return
  it. No sandbox is booted and nothing re-runs.
- **diff**: diff two checkpoints' trees.
- **checkout**: restore the working directory to a checkpoint and move the head
  there, so later steps build on that point.
- **fork**: create a new sandbox whose working directory starts as a
  checkpoint's exact state, from any sandbox, sharing all unchanged blobs. The
  fork records where it came from and diverges without touching the original.

## Backends

The engine talks to a `Backend` that runs a command in a working directory. The
process backend runs it as a local child. A firecracker backend runs it in a
microVM and additionally snapshots memory; the engine and every command are
unchanged, the checkpoint just carries more. See
[firecracker.md](firecracker.md).

## On-disk layout

```
.vit/
  blobs/         content-addressed blobs and trees
  sandboxes/     one json per sandbox
  checkpoints/   one json per checkpoint
  work/<id>/     the live working directory of a sandbox
  CURRENT        the current sandbox id
```
