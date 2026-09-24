package engine

import (
	"context"
	"io"

	"github.com/numinous-technology/vitvm/internal/tree"
)

// Backend runs a command in a sandbox and reports its exit code. The process
// backend runs it as a local child in the sandbox's working directory; the
// firecracker backend runs it inside the sandbox's microVM.
type Backend interface {
	Name() string
	// Exec runs command for the sandbox with env, streaming output, and returns
	// the exit code. workDir is the sandbox's host working directory, which a
	// machine backend may ignore.
	Exec(ctx context.Context, sandboxID, workDir string, command []string, env []string, stdout, stderr io.Writer) (int, error)
}

// GuestFS is implemented by backends whose sandbox files live inside a machine
// rather than in the host working directory. The engine reads the guest's
// working tree at every checkpoint, so every checkpoint carries its files
// whatever the backend: they can be shown, diffed and cold-forked without
// booting anything.
type GuestFS interface {
	// ListFiles returns the guest working tree. File entries carry the sha256
	// of their contents, computed in the guest, so the host only fetches
	// contents it does not already have.
	ListFiles(ctx context.Context, sandboxID string) ([]tree.Entry, error)
	// ReadFile returns one file's contents from the guest working tree.
	ReadFile(ctx context.Context, sandboxID, path string) ([]byte, error)
	// WriteTree replaces the guest working tree with t, reading file contents
	// through blob.
	WriteTree(ctx context.Context, sandboxID string, t *tree.Tree, blob func(hash string) ([]byte, error)) error
}

// Image is a snapshot of a running machine, as files on the host.
type Image struct {
	Memory string // guest memory
	State  string // device and vCPU state
	Disk   string // the root disk at the same instant; "" if the backend has none
}

// MemoryBackend is a Backend that also snapshots and resumes a running
// machine, so a checkpoint resumes a live process rather than replaying files.
// The engine stores the memory and disk as chunked content-addressed objects,
// so unchanged regions are stored once across steps.
type MemoryBackend interface {
	Backend
	// Boot starts a fresh machine for the sandbox. Booting a running sandbox is
	// a no-op.
	Boot(ctx context.Context, sandboxID, workDir string) error
	// Running reports whether the sandbox's machine is up.
	Running(ctx context.Context, sandboxID string) bool
	// Snapshot pauses the machine, writes its memory, state and disk under dir,
	// and resumes it, so stepping continues from where it paused.
	Snapshot(ctx context.Context, sandboxID, dir string) (Image, error)
	// Resume starts the sandbox's machine from an image, stopping any machine
	// the sandbox already has. Checkout resumes a sandbox's own image; fork
	// resumes another sandbox's image under a new id.
	Resume(ctx context.Context, sandboxID, workDir string, img Image) error
	// Shutdown stops the sandbox's machine.
	Shutdown(ctx context.Context, sandboxID string) error
}
