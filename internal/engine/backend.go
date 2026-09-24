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
