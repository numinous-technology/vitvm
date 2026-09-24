// Package engine is vitvm itself: sandboxes that checkpoint at every step, a
// chain of checkpoints per sandbox, and the operations over them (run, log,
// checkout, diff, show, fork). A checkpoint is a tree of content-addressed
// blobs plus its parent, so any past step can be read or forked without
// re-running anything.
package engine

import "time"

// Sandbox is a working environment with a history.
type Sandbox struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Backend    string    `json:"backend"`
	Head       string    `json:"head,omitempty"`        // current checkpoint id
	ForkedFrom string    `json:"forked_from,omitempty"` // checkpoint id this sandbox began at
	Created    time.Time `json:"created"`
	Steps      int       `json:"steps"`
}

// Checkpoint is one step in a sandbox's history: the state of the sandbox
// after a command ran.
type Checkpoint struct {
	ID       string    `json:"id"`
	Sandbox  string    `json:"sandbox"`
	Seq      int       `json:"seq"`
	Parent   string    `json:"parent,omitempty"`
	TreeHash string    `json:"tree_hash"`
	Command  []string  `json:"command,omitempty"`
	ExitCode int       `json:"exit_code"`
	Created  time.Time `json:"created"`
	Added    int       `json:"added"`
	Modified int       `json:"modified"`
	Deleted  int       `json:"deleted"`
	Note     string    `json:"note,omitempty"`

	// Memory-backend fields. When the backend snapshots a running machine, a
	// checkpoint also carries the guest memory image and VM state as
	// content-addressed blobs, so a restore or a fork resumes a live process.
	// Empty for file-only (process) backends.
	MemHash   string `json:"mem_hash,omitempty"`
	StateHash string `json:"state_hash,omitempty"`
	DiskHash  string `json:"disk_hash,omitempty"`
	BaseHash  string `json:"base_hash,omitempty"` // read-only base disk under DiskHash, if layered
}

// HasMemory reports whether the checkpoint carries a resumable machine image.
func (c *Checkpoint) HasMemory() bool { return c.MemHash != "" && c.StateHash != "" }
