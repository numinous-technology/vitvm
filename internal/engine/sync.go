package engine

import (
	"encoding/json"
	"fmt"

	"github.com/numinous-technology/vitvm/internal/remote"
	"github.com/numinous-technology/vitvm/internal/tree"
)

// object key layout in a remote store (content addressed, so pushing is
// incremental and safe to repeat):
//
//	blobs/<sha>            file contents and serialised trees
//	checkpoints/<id>.json  checkpoint metadata
//	sandboxes/<id>.json    sandbox metadata
func blobKey(sha string) string      { return "blobs/" + sha }
func checkpointKey(id string) string { return "checkpoints/" + id + ".json" }
func sandboxKey(id string) string    { return "sandboxes/" + id + ".json" }

// PushStats reports what a push moved.
type PushStats struct {
	Checkpoints  int
	Blobs        int
	BlobsSkipped int
}

// Push uploads a sandbox and its full checkpoint history to the store. Blobs
// already present are not re-uploaded, so pushing the same sandbox twice moves
// only what changed.
func (e *Engine) Push(sandboxID string, store remote.ObjectStore) (*PushStats, error) {
	s, err := e.repo.Sandbox(sandboxID)
	if err != nil {
		return nil, err
	}
	hist, err := e.repo.History(s.ID)
	if err != nil {
		return nil, err
	}
	st := &PushStats{}
	for _, c := range hist {
		if err := e.pushCheckpoint(c, store, st); err != nil {
			return nil, err
		}
	}
	sb, _ := json.Marshal(s)
	if err := store.Put(sandboxKey(s.ID), sb); err != nil {
		return nil, err
	}
	return st, nil
}

func (e *Engine) pushCheckpoint(c *Checkpoint, store remote.ObjectStore, st *PushStats) error {
	// the tree blob and every file blob it references
	shas := []string{c.TreeHash}
	t, err := e.loadTree(c.TreeHash)
	if err != nil {
		return err
	}
	for _, ent := range t.Entries {
		if ent.Hash != "" {
			shas = append(shas, ent.Hash)
		}
	}
	for _, sha := range shas {
		if err := e.pushBlob(sha, store, st); err != nil {
			return err
		}
	}
	cb, _ := json.Marshal(c)
	if err := store.Put(checkpointKey(c.ID), cb); err != nil {
		return err
	}
	st.Checkpoints++
	return nil
}

func (e *Engine) pushBlob(sha string, store remote.ObjectStore, st *PushStats) error {
	has, err := store.Has(blobKey(sha))
	if err != nil {
		return err
	}
	if has {
		st.BlobsSkipped++
		return nil
	}
	data, err := e.repo.CAS().Get(sha)
	if err != nil {
		return fmt.Errorf("reading blob %s: %w", sha, err)
	}
	if err := store.Put(blobKey(sha), data); err != nil {
		return err
	}
	st.Blobs++
	return nil
}

// Pull downloads a checkpoint and everything it needs from the store into the
// local repo, so it can be read or forked here. It returns the local
// checkpoint id (unchanged). A sandbox is not created; use Fork to start one
// from the pulled checkpoint.
func (e *Engine) Pull(store remote.ObjectStore, checkpointID string) (*Checkpoint, error) {
	cb, err := store.Get(checkpointKey(checkpointID))
	if err != nil {
		if err == remote.ErrNotFound {
			return nil, fmt.Errorf("no checkpoint %s in the store", checkpointID)
		}
		return nil, err
	}
	var c Checkpoint
	if err := json.Unmarshal(cb, &c); err != nil {
		return nil, err
	}
	// the tree, then every blob it references
	tb, err := e.pullBlob(c.TreeHash, store)
	if err != nil {
		return nil, err
	}
	t, err := tree.Unmarshal(tb)
	if err != nil {
		return nil, err
	}
	for _, ent := range t.Entries {
		if ent.Hash != "" {
			if _, err := e.pullBlob(ent.Hash, store); err != nil {
				return nil, err
			}
		}
	}
	if err := e.repo.SaveCheckpoint(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (e *Engine) pullBlob(sha string, store remote.ObjectStore) ([]byte, error) {
	if e.repo.CAS().Has(sha) {
		return e.repo.CAS().Get(sha)
	}
	data, err := store.Get(blobKey(sha))
	if err != nil {
		return nil, fmt.Errorf("pulling blob %s: %w", sha, err)
	}
	got, err := e.repo.CAS().Put(data) // Put verifies the content hash
	if err != nil {
		return nil, err
	}
	if got != sha {
		return nil, fmt.Errorf("blob %s in the store hashes to %s", sha, got)
	}
	return data, nil
}
