package engine

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// FakeMemoryBackend is a MemoryBackend with no hypervisor: it runs steps as
// local processes and models the machine's memory as a byte buffer that each
// step mutates. It exists so the memory-checkpoint path (snapshot, store,
// restore, fork of live state) is exercised on any machine, and so CI can run
// it. The Firecracker backend (internal/fcvm) implements the same interface
// against a real microVM.
type FakeMemoryBackend struct {
	mu  sync.Mutex
	ram map[string][]byte // sandbox id -> "memory"
}

// NewFakeMemoryBackend returns an empty fake backend.
func NewFakeMemoryBackend() *FakeMemoryBackend {
	return &FakeMemoryBackend{ram: map[string][]byte{}}
}

// Name identifies the backend.
func (f *FakeMemoryBackend) Name() string { return "fake-memory" }

// Boot creates the machine if it does not exist.
func (f *FakeMemoryBackend) Boot(ctx context.Context, id, workDir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ram[id]; !ok {
		f.ram[id] = []byte("boot\n")
	}
	return nil
}

// Exec runs the command in workDir and appends a record to the machine's
// memory, so memory changes with each step.
func (f *FakeMemoryBackend) Exec(ctx context.Context, workDir string, command []string, env []string, stdout, stderr io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = workDir, env, stdout, stderr
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		return -1, err
	}
	return code, nil
}

// Snapshot writes the machine's memory and a small state file.
func (f *FakeMemoryBackend) Snapshot(ctx context.Context, id, dir string) (string, string, error) {
	f.mu.Lock()
	ram := append([]byte(nil), f.ram[id]...)
	f.mu.Unlock()
	memPath := filepath.Join(dir, "mem")
	statePath := filepath.Join(dir, "state")
	if err := os.WriteFile(memPath, ram, 0o600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(statePath, []byte("fake-vm-state"), 0o600); err != nil {
		return "", "", err
	}
	return memPath, statePath, nil
}

// Restore loads memory from a snapshot into the sandbox's machine.
func (f *FakeMemoryBackend) Restore(ctx context.Context, id, workDir, memPath, statePath string) error {
	data, err := os.ReadFile(memPath)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.ram[id] = data
	f.mu.Unlock()
	return nil
}

// Fork loads a snapshot into a new machine.
func (f *FakeMemoryBackend) Fork(ctx context.Context, newID, workDir, memPath, statePath string) error {
	return f.Restore(ctx, newID, workDir, memPath, statePath)
}

// Shutdown drops the machine.
func (f *FakeMemoryBackend) Shutdown(ctx context.Context, id string) error {
	f.mu.Lock()
	delete(f.ram, id)
	f.mu.Unlock()
	return nil
}

// Memory returns a sandbox's current memory, for tests to assert on.
func (f *FakeMemoryBackend) Memory(id string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.ram[id]...)
}

// Touch mutates a sandbox's memory, so a test can make two steps differ.
func (f *FakeMemoryBackend) Touch(id string, b []byte) {
	f.mu.Lock()
	f.ram[id] = append(f.ram[id], b...)
	f.mu.Unlock()
}
