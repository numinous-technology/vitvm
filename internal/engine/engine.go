package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/numinous-technology/vitvm/internal/snap"
	"github.com/numinous-technology/vitvm/internal/tree"
)

// Engine runs sandboxes and checkpoints them.
type Engine struct {
	repo    *Repo
	backend Backend
}

// New builds an engine over a repo and a backend.
func New(repo *Repo, backend Backend) *Engine {
	return &Engine{repo: repo, backend: backend}
}

// Repo exposes the underlying repo.
func (e *Engine) Repo() *Repo { return e.repo }

// Create makes a new, empty sandbox.
func (e *Engine) Create(name string) (*Sandbox, error) {
	s := &Sandbox{ID: newID("sbx-"), Name: name, Backend: e.backend.Name(), Created: time.Now().UTC()}
	if name == "" {
		s.Name = s.ID
	}
	if err := os.MkdirAll(e.repo.WorkDir(s.ID), 0o755); err != nil {
		return nil, err
	}
	// an initial empty checkpoint, so a sandbox always has a root to fork from
	c, err := e.checkpoint(context.Background(), s, nil, 0, "created")
	if err != nil {
		return nil, err
	}
	s.Head = c.ID
	return s, e.repo.SaveSandbox(s)
}

// Run executes a command in the sandbox and checkpoints the result. It is the
// heart of vitvm: every step leaves a checkpoint you can return to. On a
// machine backend, a sandbox whose machine is not running is first brought
// back from its head checkpoint (warm if it has memory, cold from its files
// otherwise), so a sandbox survives its machine stopping or the host
// rebooting.
func (e *Engine) Run(ctx context.Context, s *Sandbox, command []string, stdout, stderr io.Writer) (*Checkpoint, error) {
	if err := e.ensureMachine(ctx, s); err != nil {
		return nil, err
	}
	code, err := e.backend.Exec(ctx, s.ID, e.repo.WorkDir(s.ID), command, os.Environ(), stdout, stderr)
	if err != nil {
		return nil, err
	}
	s.Steps++
	c, err := e.checkpoint(ctx, s, command, code, "")
	if err != nil {
		return nil, err
	}
	s.Head = c.ID
	return c, e.repo.SaveSandbox(s)
}

// Stop shuts down the sandbox's machine, if it has one. Its history is kept;
// the next Run resumes from the head checkpoint.
func (e *Engine) Stop(ctx context.Context, s *Sandbox) error {
	if mb, ok := e.memBackend(); ok {
		return mb.Shutdown(ctx, s.ID)
	}
	return nil
}

// checkpoint records the sandbox's current state on top of its head: the
// working tree always (read from the guest on a machine backend), and for a
// real step on a memory backend the machine's memory, state and disk too.
func (e *Engine) checkpoint(ctx context.Context, s *Sandbox, command []string, code int, note string) (*Checkpoint, error) {
	var t *tree.Tree
	var err error
	if g, ok := e.backend.(GuestFS); ok && e.machineRunning(ctx, s) {
		t, err = e.guestTree(ctx, g, s.ID)
	} else {
		t, err = tree.Snapshot(e.repo.WorkDir(s.ID), e.repo.CAS(), defaultIgnore)
	}
	if err != nil {
		return nil, err
	}
	tb, err := t.Marshal()
	if err != nil {
		return nil, err
	}
	treeHash, err := e.repo.CAS().Put(tb)
	if err != nil {
		return nil, err
	}
	var parentTree *tree.Tree
	if s.Head != "" {
		if pc, err := e.repo.Checkpoint(s.Head); err == nil {
			parentTree, _ = e.loadTree(pc.TreeHash)
		}
	}
	changes := tree.Diff(parentTree, t)
	c := &Checkpoint{ID: newID("ck-"), Sandbox: s.ID, Seq: s.Steps, Parent: s.Head,
		TreeHash: treeHash, Command: command, ExitCode: code, Created: time.Now().UTC(), Note: note}
	for _, ch := range changes {
		switch ch.Kind {
		case "added":
			c.Added++
		case "modified":
			c.Modified++
		case "deleted":
			c.Deleted++
		}
	}
	if command != nil {
		if err := e.captureMachine(ctx, s, c); err != nil {
			return nil, err
		}
	}
	return c, e.repo.SaveCheckpoint(c)
}

func (e *Engine) loadTree(hash string) (*tree.Tree, error) {
	b, err := e.repo.CAS().Get(hash)
	if err != nil {
		return nil, err
	}
	return tree.Unmarshal(b)
}

// Tree returns the file tree captured at a checkpoint, without running
// anything.
func (e *Engine) Tree(checkpointID string) (*tree.Tree, error) {
	c, err := e.repo.Checkpoint(checkpointID)
	if err != nil {
		return nil, err
	}
	return e.loadTree(c.TreeHash)
}

// ReadFile returns the contents of a path as captured at a checkpoint, without
// booting or re-running. This is the "browse any past step" property.
func (e *Engine) ReadFile(checkpointID, path string) ([]byte, error) {
	t, err := e.Tree(checkpointID)
	if err != nil {
		return nil, err
	}
	ent, ok := t.Find(path)
	if !ok {
		return nil, fmt.Errorf("%s is not in checkpoint %s", path, checkpointID)
	}
	if ent.Link != "" {
		return []byte("-> " + ent.Link + "\n"), nil
	}
	if ent.Dir {
		return nil, fmt.Errorf("%s is a directory", path)
	}
	return e.repo.CAS().Get(ent.Hash)
}

// Diff reports what changed between two checkpoints. With one argument it
// compares a checkpoint against its parent.
func (e *Engine) Diff(fromID, toID string) ([]tree.Change, error) {
	to, err := e.Tree(toID)
	if err != nil {
		return nil, err
	}
	var from *tree.Tree
	if fromID != "" {
		if from, err = e.Tree(fromID); err != nil {
			return nil, err
		}
	}
	return tree.Diff(from, to), nil
}

// Checkout moves the sandbox back to a checkpoint. The working tree is
// restored; on a machine backend the machine resumes from the checkpoint's
// image when it has one (processes and all), and otherwise the guest's files
// are rewritten to the checkpoint's tree. Later steps build on this point.
func (e *Engine) Checkout(s *Sandbox, checkpointID string) error {
	ctx := context.Background()
	c, err := e.repo.Checkpoint(checkpointID)
	if err != nil {
		return err
	}
	if c.Sandbox != s.ID {
		return fmt.Errorf("checkpoint %s belongs to another sandbox; use fork", checkpointID)
	}
	t, err := e.loadTree(c.TreeHash)
	if err != nil {
		return err
	}
	if err := snap.Restore(t, e.repo.CAS(), e.repo.WorkDir(s.ID)); err != nil {
		return err
	}
	if err := e.bringUp(ctx, s.ID, c, t); err != nil {
		return err
	}
	s.Head = c.ID
	return e.repo.SaveSandbox(s)
}

// Fork makes a new sandbox that starts at a checkpoint, from any sandbox. On a
// machine backend a checkpoint with an image forks warm: the new machine
// resumes the original's memory and disk and the two diverge from that live
// point. Otherwise it forks cold from the checkpoint's files. The two sandboxes
// share every unchanged blob, so a fork is cheap.
func (e *Engine) Fork(fromCheckpointID, name string) (*Sandbox, error) {
	ctx := context.Background()
	src, err := e.repo.Checkpoint(fromCheckpointID)
	if err != nil {
		return nil, err
	}
	t, err := e.loadTree(src.TreeHash)
	if err != nil {
		return nil, err
	}
	s := &Sandbox{ID: newID("sbx-"), Name: name, Backend: e.backend.Name(),
		ForkedFrom: fromCheckpointID, Created: time.Now().UTC()}
	if name == "" {
		s.Name = s.ID
	}
	if err := snap.Restore(t, e.repo.CAS(), e.repo.WorkDir(s.ID)); err != nil {
		return nil, err
	}
	if err := e.bringUp(ctx, s.ID, src, t); err != nil {
		return nil, err
	}
	// the fork's root checkpoint carries the same tree and image, under a new
	// lineage
	root := &Checkpoint{ID: newID("ck-"), Sandbox: s.ID, Seq: 0, TreeHash: src.TreeHash,
		MemHash: src.MemHash, StateHash: src.StateHash, DiskHash: src.DiskHash,
		Created: time.Now().UTC(), Note: "forked from " + fromCheckpointID}
	if err := e.repo.SaveCheckpoint(root); err != nil {
		return nil, err
	}
	s.Head = root.ID
	return s, e.repo.SaveSandbox(s)
}

// defaultIgnore keeps vitvm's own bookkeeping and common noise out of
// checkpoints.
func defaultIgnore(rel string, isDir bool) bool {
	base := rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		base = rel[i+1:]
	}
	return base == ".git" || base == ".vit" || rel == "lost+found"
}
