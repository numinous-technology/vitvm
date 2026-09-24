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
	c, err := e.checkpoint(s, nil, 0, "created")
	if err != nil {
		return nil, err
	}
	s.Head = c.ID
	return s, e.repo.SaveSandbox(s)
}

// Run executes a command in the sandbox and checkpoints the result. It is the
// heart of vitvm: every step leaves a checkpoint you can return to.
func (e *Engine) Run(ctx context.Context, s *Sandbox, command []string, stdout, stderr io.Writer) (*Checkpoint, error) {
	code, err := e.backend.Exec(ctx, e.repo.WorkDir(s.ID), command, os.Environ(), stdout, stderr)
	if err != nil {
		return nil, err
	}
	s.Steps++
	c, err := e.checkpoint(s, command, code, "")
	if err != nil {
		return nil, err
	}
	s.Head = c.ID
	return c, e.repo.SaveSandbox(s)
}

// checkpoint snapshots the sandbox's working directory into a new checkpoint
// on top of its current head.
func (e *Engine) checkpoint(s *Sandbox, command []string, code int, note string) (*Checkpoint, error) {
	t, err := tree.Snapshot(e.repo.WorkDir(s.ID), e.repo.CAS(), defaultIgnore)
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

// Checkout restores the sandbox's working directory to a checkpoint's state
// and moves its head there. Later steps build on this point.
func (e *Engine) Checkout(s *Sandbox, checkpointID string) error {
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
	s.Head = c.ID
	return e.repo.SaveSandbox(s)
}

// Fork makes a new sandbox whose working directory starts as the exact state
// of a checkpoint, from any sandbox. The two share every unchanged blob, so a
// fork is cheap. This is the "branch from step N" operation.
func (e *Engine) Fork(fromCheckpointID, name string) (*Sandbox, error) {
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
	// the fork's root checkpoint carries the same tree, but a new lineage
	root := &Checkpoint{ID: newID("ck-"), Sandbox: s.ID, Seq: 0, TreeHash: src.TreeHash,
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
