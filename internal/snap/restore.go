// Package snap materialises a tree back onto disk. Snapshotting lives in the
// tree package; restoring is its inverse and is what `vit checkout` and a fork
// use to lay a checkpoint down as a working directory.
package snap

import (
	"io"
	"os"
	"path/filepath"

	"github.com/numinous-technology/vitvm/internal/tree"
)

// Blobs reads file contents by hash.
type Blobs interface {
	OpenBlob(hash string) (io.ReadCloser, error)
}

// Restore writes the tree into dst, which is created if absent and cleared of
// anything not in the tree if it already exists. Directories come first so
// files and links land in place.
func Restore(t *tree.Tree, b Blobs, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if err := clearExtra(t, dst); err != nil {
		return err
	}
	for _, e := range t.Entries {
		if e.Dir {
			if err := os.MkdirAll(filepath.Join(dst, e.Path), os.FileMode(e.Mode)); err != nil {
				return err
			}
		}
	}
	for _, e := range t.Entries {
		p := filepath.Join(dst, e.Path)
		switch {
		case e.Dir:
			continue
		case e.Link != "":
			os.Remove(p)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(e.Link, p); err != nil {
				return err
			}
		default:
			if err := writeBlob(b, e.Hash, p, os.FileMode(e.Mode)); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeBlob(b Blobs, hash, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	rc, err := b.OpenBlob(hash)
	if err != nil {
		return err
	}
	defer rc.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, rc); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// clearExtra removes files and dirs under dst that the tree does not contain,
// so a checkout is exact rather than additive.
func clearExtra(t *tree.Tree, dst string) error {
	want := map[string]bool{}
	for _, e := range t.Entries {
		want[filepath.FromSlash(e.Path)] = true
	}
	var toRemove []string
	filepath.WalkDir(dst, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == dst {
			return nil
		}
		rel, _ := filepath.Rel(dst, p)
		if !want[rel] {
			toRemove = append(toRemove, p)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	for _, p := range toRemove {
		os.RemoveAll(p)
	}
	return nil
}
