package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/numinous-technology/vitvm/internal/cas"
)

// Repo is an on-disk vitvm store: blobs, checkpoint and sandbox metadata, and
// the live working directories.
//
//	<root>/blobs/         content-addressed blobs and trees
//	<root>/sandboxes/     one json per sandbox
//	<root>/checkpoints/   one json per checkpoint
//	<root>/work/<id>/     the live working directory of a sandbox
type Repo struct {
	root string
	cas  *cas.Store
}

// OpenRepo opens or creates a repo at root.
func OpenRepo(root string) (*Repo, error) {
	for _, d := range []string{"blobs", "sandboxes", "checkpoints", "work"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			return nil, err
		}
	}
	store, err := cas.Open(filepath.Join(root, "blobs"))
	if err != nil {
		return nil, err
	}
	return &Repo{root: root, cas: store}, nil
}

// CAS exposes the blob store.
func (r *Repo) CAS() *cas.Store { return r.cas }

// WorkDir is where a sandbox's live files sit.
func (r *Repo) WorkDir(sandboxID string) string {
	return filepath.Join(r.root, "work", sandboxID)
}

func newID(prefix string) string {
	var b [6]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func (r *Repo) putJSON(dir, id string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.root, dir, id+".json"), b, 0o644)
}

func (r *Repo) getJSON(dir, id string, v any) error {
	b, err := os.ReadFile(filepath.Join(r.root, dir, id+".json"))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// SaveSandbox persists a sandbox.
func (r *Repo) SaveSandbox(s *Sandbox) error { return r.putJSON("sandboxes", s.ID, s) }

// Sandbox loads a sandbox by id or name.
func (r *Repo) Sandbox(idOrName string) (*Sandbox, error) {
	var s Sandbox
	if err := r.getJSON("sandboxes", idOrName, &s); err == nil {
		return &s, nil
	}
	for _, sb := range r.Sandboxes() {
		if sb.Name == idOrName {
			return sb, nil
		}
	}
	return nil, fmt.Errorf("no sandbox %q", idOrName)
}

// Sandboxes lists all sandboxes, newest first.
func (r *Repo) Sandboxes() []*Sandbox {
	var out []*Sandbox
	entries, _ := os.ReadDir(filepath.Join(r.root, "sandboxes"))
	for _, e := range entries {
		var s Sandbox
		if r.getJSON("sandboxes", trimJSON(e.Name()), &s) == nil {
			cp := s
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// SaveCheckpoint persists a checkpoint.
func (r *Repo) SaveCheckpoint(c *Checkpoint) error { return r.putJSON("checkpoints", c.ID, c) }

// Checkpoint loads a checkpoint.
func (r *Repo) Checkpoint(id string) (*Checkpoint, error) {
	var c Checkpoint
	if err := r.getJSON("checkpoints", id, &c); err != nil {
		return nil, fmt.Errorf("no checkpoint %q", id)
	}
	return &c, nil
}

// History returns a sandbox's checkpoints from head back to root.
func (r *Repo) History(sandboxID string) ([]*Checkpoint, error) {
	s, err := r.Sandbox(sandboxID)
	if err != nil {
		return nil, err
	}
	var chain []*Checkpoint
	for id := s.Head; id != ""; {
		c, err := r.Checkpoint(id)
		if err != nil {
			return nil, err
		}
		chain = append(chain, c)
		id = c.Parent
	}
	return chain, nil
}

func trimJSON(name string) string {
	return name[:len(name)-len(".json")]
}
