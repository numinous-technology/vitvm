// Package cas is a content-addressed store: bytes go in, their sha256 comes
// back, and identical bytes are stored once. It is the floor vitvm is built
// on. A checkpoint's files, and later a memory image's pages, are blobs here,
// so a file that never changes across a thousand checkpoints costs one copy.
package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Store is a directory of content-addressed blobs.
type Store struct{ root string }

// Open returns a store rooted at dir, creating it if needed.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{root: dir}, nil
}

func (s *Store) path(hash string) string {
	if len(hash) < 2 {
		return filepath.Join(s.root, hash)
	}
	return filepath.Join(s.root, hash[:2], hash[2:])
}

// Put stores b and returns its hash. Storing the same bytes twice is a no-op.
func (s *Store) Put(b []byte) (string, error) {
	sum := sha256.Sum256(b)
	hash := hex.EncodeToString(sum[:])
	p := s.path(hash)
	if _, err := os.Stat(p); err == nil {
		return hash, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	// a unique temp file per writer: concurrent writers of the same blob write
	// identical bytes, so whichever rename lands last is fine
	f, err := os.CreateTemp(filepath.Dir(p), ".put-*")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	os.Chmod(f.Name(), 0o644)
	if err := os.Rename(f.Name(), p); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return hash, nil
}

// PutFile stores a file's contents by streaming it, returning the hash and
// size without holding the whole file in memory.
func (s *Store) PutFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	dst := s.path(hash)
	if _, err := os.Stat(dst); err == nil {
		return hash, n, nil
	}
	if _, err := f.Seek(0, 0); err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", 0, err
	}
	out, err := os.CreateTemp(filepath.Dir(dst), ".put-*")
	if err != nil {
		return "", 0, err
	}
	tmp := out.Name()
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		return "", 0, err
	}
	out.Close()
	os.Chmod(tmp, 0o644)
	return hash, n, os.Rename(tmp, dst)
}

// Get returns the bytes for a hash.
func (s *Store) Get(hash string) ([]byte, error) {
	b, err := os.ReadFile(s.path(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// OpenBlob returns a reader for a hash, so a large blob need not be held in
// memory.
func (s *Store) OpenBlob(hash string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(hash))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Has reports whether a hash is present.
func (s *Store) Has(hash string) bool {
	_, err := os.Stat(s.path(hash))
	return err == nil
}

// ErrNotFound is returned when a hash is absent.
var ErrNotFound = errors.New("blob not found")

func hashOf(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
