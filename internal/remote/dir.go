package remote

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// DirStore is an ObjectStore backed by a local directory, for a shared
// filesystem (NFS, a mounted bucket) or for tests. Keys map to paths under
// root.
type DirStore struct{ root string }

// NewDirStore opens a directory store.
func NewDirStore(root string) (*DirStore, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &DirStore{root: root}, nil
}

func (d *DirStore) path(key string) string { return filepath.Join(d.root, filepath.FromSlash(key)) }

// Put writes the object.
func (d *DirStore) Put(key string, data []byte) error {
	p := d.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Get reads the object.
func (d *DirStore) Get(key string) ([]byte, error) {
	b, err := os.ReadFile(d.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// Has reports presence.
func (d *DirStore) Has(key string) (bool, error) {
	_, err := os.Stat(d.path(key))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// List returns keys under prefix.
func (d *DirStore) List(prefix string) ([]string, error) {
	base := filepath.Join(d.root, filepath.FromSlash(prefix))
	var keys []string
	filepath.Walk(d.root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".tmp") {
			return nil
		}
		if !strings.HasPrefix(p, base) {
			return nil
		}
		rel, _ := filepath.Rel(d.root, p)
		keys = append(keys, filepath.ToSlash(rel))
		return nil
	})
	return keys, nil
}
