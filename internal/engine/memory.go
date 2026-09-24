package engine

import (
	"context"
	"os"
	"path/filepath"
)

// memBackend returns the backend as a MemoryBackend if it is one.
func (e *Engine) memBackend() (MemoryBackend, bool) {
	mb, ok := e.backend.(MemoryBackend)
	return mb, ok
}

// captureMemory, for a memory backend, snapshots the running machine and stores
// its memory image and VM state as content-addressed blobs, returning their
// hashes. It is called during a checkpoint, after the file tree is taken.
func (e *Engine) captureMemory(ctx context.Context, s *Sandbox) (memHash, stateHash string, err error) {
	mb, ok := e.memBackend()
	if !ok {
		return "", "", nil
	}
	dir, err := os.MkdirTemp("", "vit-snap-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)
	memPath, statePath, err := mb.Snapshot(ctx, s.ID, dir)
	if err != nil {
		return "", "", err
	}
	memHash, _, err = e.repo.CAS().PutFile(memPath)
	if err != nil {
		return "", "", err
	}
	stateHash, _, err = e.repo.CAS().PutFile(statePath)
	if err != nil {
		return "", "", err
	}
	return memHash, stateHash, nil
}

// restoreMemory materialises a checkpoint's memory image and state to temp
// files and resumes (or forks) the machine from them. workDir is the target
// sandbox's working directory.
func (e *Engine) restoreMemory(ctx context.Context, c *Checkpoint, targetID, workDir string, fork bool) error {
	mb, ok := e.memBackend()
	if !ok || !c.HasMemory() {
		return nil
	}
	dir, err := os.MkdirTemp("", "vit-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	memPath := filepath.Join(dir, "mem")
	statePath := filepath.Join(dir, "state")
	if err := e.writeBlobTo(c.MemHash, memPath); err != nil {
		return err
	}
	if err := e.writeBlobTo(c.StateHash, statePath); err != nil {
		return err
	}
	if fork {
		return mb.Fork(ctx, targetID, workDir, memPath, statePath)
	}
	return mb.Restore(ctx, targetID, workDir, memPath, statePath)
}

func (e *Engine) writeBlobTo(hash, path string) error {
	data, err := e.repo.CAS().Get(hash)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
