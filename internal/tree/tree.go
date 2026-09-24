// Package tree is a snapshot of a directory as a sorted list of entries, each
// pointing at a blob in the store. Two trees can be diffed to see what a step
// changed. A tree is itself serialisable, so a checkpoint is just a tree hash
// plus its parent, and reading a checkpoint's files never touches the running
// sandbox.
package tree

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one path in a snapshot.
type Entry struct {
	Path string `json:"path"`           // relative, slash-separated
	Mode uint32 `json:"mode"`           // unix mode bits
	Size int64  `json:"size"`           // bytes (0 for dirs and links)
	Hash string `json:"hash,omitempty"` // blob hash of file contents
	Link string `json:"link,omitempty"` // symlink target, if a symlink
	Dir  bool   `json:"dir,omitempty"`  // a directory
}

// Tree is a full snapshot: entries sorted by path.
type Tree struct {
	Entries []Entry `json:"entries"`
}

// Blober stores file contents and returns their hash.
type Blober interface {
	PutFile(path string) (string, int64, error)
}

// Ignore decides whether a path (relative, slash-separated) is left out of the
// snapshot. It is given directories too, so a whole subtree can be skipped.
type Ignore func(rel string, isDir bool) bool

// Snapshot walks root, stores every file's contents in b, and returns the
// tree. Symlinks are recorded by target, not followed. Sockets, devices and
// pipes are skipped.
func Snapshot(root string, b Blober, ignore Ignore) (*Tree, error) {
	var entries []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if ignore != nil && ignore(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := uint32(info.Mode().Perm())
		switch {
		case d.IsDir():
			entries = append(entries, Entry{Path: rel, Mode: mode, Dir: true})
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			entries = append(entries, Entry{Path: rel, Mode: mode, Link: target})
		case info.Mode().IsRegular():
			hash, size, err := b.PutFile(p)
			if err != nil {
				return err
			}
			entries = append(entries, Entry{Path: rel, Mode: mode, Size: size, Hash: hash})
		default:
			// skip sockets, devices, fifos
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return &Tree{Entries: entries}, nil
}

// Marshal encodes a tree as canonical JSON.
func (t *Tree) Marshal() ([]byte, error) { return json.Marshal(t) }

// Unmarshal decodes a tree.
func Unmarshal(b []byte) (*Tree, error) {
	var t Tree
	return &t, json.Unmarshal(b, &t)
}

// Find returns the entry for a path, if present.
func (t *Tree) Find(path string) (Entry, bool) {
	path = strings.TrimPrefix(filepath.ToSlash(path), "./")
	for _, e := range t.Entries {
		if e.Path == path {
			return e, true
		}
	}
	return Entry{}, false
}

// Change is one path that differs between two trees.
type Change struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // added, modified, deleted
}

// Diff reports what changed going from old to new. A nil old tree means every
// entry is added.
func Diff(old, new *Tree) []Change {
	oldByPath := map[string]Entry{}
	if old != nil {
		for _, e := range old.Entries {
			oldByPath[e.Path] = e
		}
	}
	newByPath := map[string]Entry{}
	for _, e := range new.Entries {
		newByPath[e.Path] = e
	}
	var changes []Change
	for _, e := range new.Entries {
		prev, ok := oldByPath[e.Path]
		if !ok {
			changes = append(changes, Change{Path: e.Path, Kind: "added"})
		} else if !sameEntry(prev, e) {
			changes = append(changes, Change{Path: e.Path, Kind: "modified"})
		}
	}
	if old != nil {
		for _, e := range old.Entries {
			if _, ok := newByPath[e.Path]; !ok {
				changes = append(changes, Change{Path: e.Path, Kind: "deleted"})
			}
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

func sameEntry(a, b Entry) bool {
	return a.Mode == b.Mode && a.Size == b.Size && a.Hash == b.Hash && a.Link == b.Link && a.Dir == b.Dir
}

// Sorted returns entries sorted by path, the order a Tree keeps them in.
func Sorted(entries []Entry) []Entry {
	out := append([]Entry(nil), entries...)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
