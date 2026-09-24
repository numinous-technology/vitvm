# How vitvm works

Four small pieces: a blob store, a tree, a checkpoint chain, and an engine that
runs commands and snapshots them.

## The blob store (`internal/cas`)

Content-addressed: you hand it bytes, it returns their sha256 and stores them
once. Two files with the same contents, anywhere in any sandbox at any step,
are one object on disk. Large files are streamed, not held in memory.

Machine images (a VM's memory and disk) are stored chunked: split into 1 MiB
pieces, each a blob, plus a manifest listing them. A step that changes a few
pages of memory adds a few chunks, and all-zero regions are one chunk.

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

A checkpoint records its sandbox, step number, parent, the hash of its tree,
the command and exit code, and the change counts. On the firecracker backend it
also records the manifests of the machine's memory and disk and the hash of its
device state. A sandbox points at its head checkpoint; walking parents from
the head gives the history.

## The engine

- **run**: execute the command through the backend, then checkpoint on top of
  the head. The tree comes from the host working directory on the process
  backend and from the guest's `/work` on the firecracker backend; a
  firecracker step also snapshots the machine. A firecracker sandbox whose
  machine is not running is first brought back from its head checkpoint.
- **show / diff**: resolve a checkpoint's tree from the store. No machine is
  booted and nothing re-runs, whatever the backend.
- **checkout**: move the head back to a checkpoint. Files are restored; a
  machine resumes from the checkpoint's image, or has the checkpoint's files
  written into it when there is no image.
- **fork**: a new sandbox starting at a checkpoint from any sandbox, warm from
  an image or cold from files, sharing every unchanged blob and chunk.
- **stop**: shut a sandbox's machine down; its history is untouched.

## Backends

The engine drives a `Backend` that runs a command. A `GuestFS` backend also
lists, reads and writes the sandbox's files inside the machine, and a
`MemoryBackend` boots, snapshots and resumes the machine. The process backend is
just a `Backend`; the firecracker backend is all three. See
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
