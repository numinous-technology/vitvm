package engine

import (
	"context"
	"io"
)

// Backend runs a command in a sandbox's working directory and reports its exit
// code. The process backend runs it as a local child; a firecracker backend
// (see docs/firecracker.md) runs it inside a microVM and additionally captures
// memory, so a checkpoint restores a live process, not just files.
type Backend interface {
	Name() string
	// Exec runs command in workDir with env, streaming output, and returns the
	// exit code.
	Exec(ctx context.Context, workDir string, command []string, env []string, stdout, stderr io.Writer) (int, error)
}

// MemoryBackend is a Backend that also snapshots and restores a running
// machine, so a checkpoint resumes a live process rather than replaying files.
// The engine stores the memory image and VM state it returns as
// content-addressed blobs, deduplicated across steps like any other blob.
//
// A backend that implements this is driven so that its working directory (a
// shared filesystem with the guest) is still captured as the file tree, and
// the memory image and state ride alongside in the same checkpoint.
type MemoryBackend interface {
	Backend
	// Boot starts the sandbox's machine, using workDir as the guest's shared
	// working directory. Idempotent: booting an already-running sandbox is a
	// no-op.
	Boot(ctx context.Context, sandboxID, workDir string) error
	// Snapshot pauses the machine and writes its memory image and VM state to
	// files under dir, returning their paths. The machine is resumed before
	// return, so stepping continues from where it paused.
	Snapshot(ctx context.Context, sandboxID, dir string) (memPath, statePath string, err error)
	// Restore resumes the sandbox's machine from a memory image and state.
	Restore(ctx context.Context, sandboxID, workDir, memPath, statePath string) error
	// Fork resumes a copy of a snapshot as a new sandbox, so two machines
	// diverge from the same live point.
	Fork(ctx context.Context, newSandboxID, workDir, memPath, statePath string) error
	// Shutdown stops the sandbox's machine.
	Shutdown(ctx context.Context, sandboxID string) error
}
