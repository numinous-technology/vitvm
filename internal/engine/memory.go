package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/numinous-technology/vitvm/internal/tree"
)

// memBackend returns the backend as a MemoryBackend if it is one.
func (e *Engine) memBackend() (MemoryBackend, bool) {
	mb, ok := e.backend.(MemoryBackend)
	return mb, ok
}

func (e *Engine) machineRunning(ctx context.Context, s *Sandbox) bool {
	mb, ok := e.memBackend()
	return ok && mb.Running(ctx, s.ID)
}

// ensureMachine makes sure a machine backend's sandbox is up before a step.
// A sandbox whose machine is gone comes back from its head checkpoint.
func (e *Engine) ensureMachine(ctx context.Context, s *Sandbox) error {
	mb, ok := e.memBackend()
	if !ok || mb.Running(ctx, s.ID) {
		return nil
	}
	if s.Head == "" {
		return mb.Boot(ctx, s.ID, e.repo.WorkDir(s.ID))
	}
	c, err := e.repo.Checkpoint(s.Head)
	if err != nil {
		return err
	}
	t, err := e.loadTree(c.TreeHash)
	if err != nil {
		return err
	}
	return e.bringUp(ctx, s.ID, c, t)
}

// bringUp starts targetID's machine at checkpoint c: warm from its image when
// it has one, otherwise a fresh machine given c's files. For a process backend
// it does nothing; the host working tree was already restored.
func (e *Engine) bringUp(ctx context.Context, targetID string, c *Checkpoint, t *tree.Tree) error {
	mb, ok := e.memBackend()
	if !ok {
		return nil
	}
	if c.HasMemory() {
		dir, err := os.MkdirTemp("", "vit-restore-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		img := Image{Memory: filepath.Join(dir, "mem"), State: filepath.Join(dir, "state")}
		if err := e.repo.CAS().GetChunkedTo(c.MemHash, img.Memory); err != nil {
			return err
		}
		if err := e.writeBlobTo(c.StateHash, img.State); err != nil {
			return err
		}
		if c.DiskHash != "" {
			img.Disk = filepath.Join(dir, "disk")
			if err := e.repo.CAS().GetChunkedTo(c.DiskHash, img.Disk); err != nil {
				return err
			}
		}
		return mb.Resume(ctx, targetID, e.repo.WorkDir(targetID), img)
	}
	// cold: a fresh machine, then the checkpoint's files
	mb.Shutdown(ctx, targetID)
	if err := mb.Boot(ctx, targetID, e.repo.WorkDir(targetID)); err != nil {
		return err
	}
	if g, ok := e.backend.(GuestFS); ok {
		return g.WriteTree(ctx, targetID, t, e.repo.CAS().Get)
	}
	return nil
}

// captureMachine snapshots a memory backend's running machine into c: memory
// and disk as chunked objects (unchanged regions shared with earlier steps),
// device state as a plain blob.
func (e *Engine) captureMachine(ctx context.Context, s *Sandbox, c *Checkpoint) error {
	mb, ok := e.memBackend()
	if !ok || !mb.Running(ctx, s.ID) {
		return nil
	}
	dir, err := os.MkdirTemp("", "vit-snap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	img, err := mb.Snapshot(ctx, s.ID, dir)
	if err != nil {
		return err
	}
	if c.MemHash, _, err = e.repo.CAS().PutChunked(img.Memory); err != nil {
		return err
	}
	if c.StateHash, _, err = e.repo.CAS().PutFile(img.State); err != nil {
		return err
	}
	if img.Disk != "" {
		if c.DiskHash, _, err = e.repo.CAS().PutChunked(img.Disk); err != nil {
			return err
		}
	}
	return nil
}

// guestTree reads the guest's working tree into a tree, fetching only file
// contents the store does not already hold and checking each against the
// hash the guest reported.
func (e *Engine) guestTree(ctx context.Context, g GuestFS, id string) (*tree.Tree, error) {
	entries, err := g.ListFiles(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("listing guest files: %w", err)
	}
	for _, ent := range entries {
		if ent.Hash == "" || e.repo.CAS().Has(ent.Hash) {
			continue
		}
		data, err := g.ReadFile(ctx, id, ent.Path)
		if err != nil {
			return nil, fmt.Errorf("reading guest file %s: %w", ent.Path, err)
		}
		got, err := e.repo.CAS().Put(data)
		if err != nil {
			return nil, err
		}
		if got != ent.Hash {
			return nil, fmt.Errorf("guest file %s changed while it was read", ent.Path)
		}
	}
	return &tree.Tree{Entries: tree.Sorted(entries)}, nil
}

func (e *Engine) writeBlobTo(hash, path string) error {
	data, err := e.repo.CAS().Get(hash)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
