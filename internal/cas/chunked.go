package cas

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Large images (a VM's memory, its disk) change in a few places between
// checkpoints. Storing each as one blob would copy the whole image every step,
// so they are split into fixed-size chunks, each a blob, and described by a
// small manifest. Unchanged chunks, and all-zero chunks, are stored once.
// Fixed-size chunks suit page-aligned images: a changed page dirties exactly
// one chunk and shifts nothing.

// ChunkSize is the size of each chunk of a chunked object.
const ChunkSize = 1 << 20

// Manifest describes a chunked object.
type Manifest struct {
	Chunked int      `json:"chunked"` // always 1; marks the blob as a manifest
	Size    int64    `json:"size"`
	Chunk   int      `json:"chunk"`
	Chunks  []string `json:"chunks"`
}

// PutChunked stores the file at path as chunks and returns the hash of its
// manifest, plus how many chunks were new to the store.
func (s *Store) PutChunked(path string) (hash string, newChunks int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	m := Manifest{Chunked: 1, Chunk: ChunkSize}
	buf := make([]byte, ChunkSize)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			sum := hashOf(buf[:n])
			if !s.Has(sum) {
				if _, err := s.Put(buf[:n]); err != nil {
					return "", 0, err
				}
				newChunks++
			}
			m.Chunks = append(m.Chunks, sum)
			m.Size += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
	}
	b, _ := json.Marshal(m)
	hash, err = s.Put(b)
	return hash, newChunks, err
}

// ReadManifest loads the manifest of a chunked object.
func (s *Store) ReadManifest(hash string) (*Manifest, error) {
	b, err := s.Get(hash)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil || m.Chunked != 1 {
		return nil, fmt.Errorf("%s is not a chunked object", hash)
	}
	return &m, nil
}

// GetChunkedTo reassembles a chunked object into a file at path.
func (s *Store) GetChunkedTo(hash, path string) error {
	m, err := s.ReadManifest(hash)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	for _, c := range m.Chunks {
		b, err := s.Get(c)
		if err != nil {
			out.Close()
			os.Remove(tmp)
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("chunk %s of %s is missing", c, hash)
			}
			return err
		}
		if _, err := out.Write(b); err != nil {
			out.Close()
			return err
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
