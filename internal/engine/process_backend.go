package engine

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

// ProcessBackend runs each step as a local child process in the sandbox's
// working directory. Checkpoints capture the directory. This is the backend
// that works on any machine, with no hypervisor.
type ProcessBackend struct{}

// Name identifies the backend.
func (ProcessBackend) Name() string { return "process" }

// Exec runs the command and returns its exit code.
func (ProcessBackend) Exec(ctx context.Context, sandboxID, workDir string, command []string, env []string, stdout, stderr io.Writer) (int, error) {
	if len(command) == 0 {
		return 0, errors.New("empty command")
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}
