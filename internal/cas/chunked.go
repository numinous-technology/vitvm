package cas

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"syscall"
)

// Large images (a VM's memory, its disk) change in a few places between
// checkpoints. Storing each as one blob would copy the whole image every step,
// so they are split into fixed-size chunks, each a blob, and described by a
// small manifest. Unchanged chunks, and all-zero chunks, are stored once.
// Fixed-size chunks suit page-aligned images: a changed page dirties exactly
// one chunk and shifts nothing.
//
// Three things keep this fast. Holes in sparse files are never read: a chunk
// that lies entirely in a hole is the zero chunk. Chunks are hashed on every
// core. And an overlay (PutChunkedOverlay) takes a sparse file holding only
// the pages that changed, such as a Firecracker diff snapshot, and reuses the
// parent image's chunks everywhere else, so a step costs in proportion to what
// it changed.

// ChunkSize is the size of each chunk of a chunked object.
const ChunkSize = 1 << 20

// Manifest describes a chunked object.
type Manifest struct {
	Chunked int      `json:"chunked"` // always 1; marks the blob as a manifest
	Size    int64    `json:"size"`
	Chunk   int      `json:"chunk"`
	Chunks  []string `json:"chunks"`
}

// zeroHashes memoises the hash of n zero bytes: a sparse 8 GiB disk has
// thousands of empty chunks, and hashing each would cost more than the data.
var zeroHashes sync.Map // int -> string

// zeroHash returns the hash of n zero bytes, storing that chunk once.
func (s *Store) zeroHash(n int) (string, error) {
	if h, ok := zeroHashes.Load(n); ok && s.Has(h.(string)) {
		return h.(string), nil
	}
	h, err := s.Put(make([]byte, n))
	if err == nil {
		zeroHashes.Store(n, h)
	}
	return h, err
}

// span is a [start, end) byte range.
type span struct{ start, end int64 }

// dataSpans lists the regions of f that hold data, using SEEK_DATA and
// SEEK_HOLE. ENXIO means the rest of the file is a hole. Where the filesystem
// cannot say, the whole file is treated as data.
func dataSpans(f *os.File, size int64) []span {
	const seekData, seekHole = 3, 4
	fd := int(f.Fd())
	var out []span
	for off := int64(0); off < size; {
		start, err := syscall.Seek(fd, off, seekData)
		if err == syscall.ENXIO {
			break
		}
		if err != nil {
			return []span{{0, size}}
		}
		end, err := syscall.Seek(fd, start, seekHole)
		if err != nil || end > size {
			end = size
		}
		out = append(out, span{start, end})
		off = end
	}
	return out
}

// overlaps reports whether any span intersects [a, b).
func overlaps(spans []span, a, b int64) bool {
	for _, sp := range spans {
		if sp.start < b && sp.end > a {
			return true
		}
	}
	return false
}

func chunkCount(size int64) int { return int((size + ChunkSize - 1) / ChunkSize) }

func chunkLen(size int64, i int) int {
	if rem := size - int64(i)*ChunkSize; rem < ChunkSize {
		return int(rem)
	}
	return ChunkSize
}

// parallel runs fn(i) for i in [0, n) on every core and returns the first
// error.
func parallel(n int, fn func(i int) error) error {
	workers := runtime.GOMAXPROCS(0)
	if workers > n {
		workers = n
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		next int
		ferr error
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				if next >= n || ferr != nil {
					mu.Unlock()
					return
				}
				i := next
				next++
				mu.Unlock()
				if err := fn(i); err != nil {
					mu.Lock()
					if ferr == nil {
						ferr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return ferr
}

// storeChunk stores b if absent and returns its hash; new reports whether it
// was new to the store.
func (s *Store) storeChunk(b []byte) (hash string, isNew bool, err error) {
	hash = hashOf(b)
	if s.Has(hash) {
		return hash, false, nil
	}
	_, err = s.Put(b)
	return hash, true, err
}

func (s *Store) putManifest(m *Manifest) (string, error) {
	b, _ := json.Marshal(m)
	return s.Put(b)
}

// PutChunked stores the file at path as chunks and returns the hash of its
// manifest, plus how many chunks were new to the store.
func (s *Store) PutChunked(path string) (hash string, newChunks int, err error) {
	return s.putChunked(path, nil)
}

// PutChunkedOverlay stores a new image made of base with the data regions of
// the sparse file at path laid over it. Chunks the overlay does not touch keep
// base's hashes without being read. This is how a diff snapshot, which holds
// only the pages dirtied since base, becomes a complete image.
func (s *Store) PutChunkedOverlay(path, baseHash string) (hash string, newChunks int, err error) {
	base, err := s.ReadManifest(baseHash)
	if err != nil {
		return "", 0, err
	}
	return s.putChunked(path, base)
}

func (s *Store) putChunked(path string, base *Manifest) (string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	size := fi.Size()
	spans := dataSpans(f, size)
	n := chunkCount(size)
	m := &Manifest{Chunked: 1, Chunk: ChunkSize, Size: size, Chunks: make([]string, n)}
	var newMu sync.Mutex
	fresh := map[string]bool{} // distinct chunks this call added to the store
	err = parallel(n, func(i int) error {
		off := int64(i) * ChunkSize
		ln := chunkLen(size, i)
		touched := overlaps(spans, off, off+int64(ln))
		if base != nil && i < len(base.Chunks) && chunkLen(base.Size, i) == ln {
			if !touched {
				m.Chunks[i] = base.Chunks[i]
				return nil
			}
			// overlay the dirty regions of this chunk onto the base chunk
			b, err := s.Get(base.Chunks[i])
			if err != nil {
				return fmt.Errorf("base chunk %d: %w", i, err)
			}
			for _, sp := range spans {
				a, e := max64(sp.start, off), min64(sp.end, off+int64(ln))
				if a < e {
					if _, err := f.ReadAt(b[a-off:e-off], a); err != nil && err != io.EOF {
						return err
					}
				}
			}
			h, isNew, err := s.storeChunk(b)
			m.Chunks[i] = h
			if isNew {
				newMu.Lock()
				fresh[h] = true
				newMu.Unlock()
			}
			return err
		}
		if !touched {
			h, err := s.zeroHash(ln)
			m.Chunks[i] = h
			return err
		}
		b := make([]byte, ln)
		if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
			return err
		}
		h, isNew, err := s.storeChunk(b)
		m.Chunks[i] = h
		if isNew {
			newMu.Lock()
			fresh[h] = true
			newMu.Unlock()
		}
		return err
	})
	if err != nil {
		return "", 0, err
	}
	hash, err := s.putManifest(m)
	return hash, len(fresh), err
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

// GetChunkedTo reassembles a chunked object into a file at path. Zero chunks
// become holes, so the file is as sparse as the image, and chunks are written
// in parallel.
func (s *Store) GetChunkedTo(hash, path string) error {
	m, err := s.ReadManifest(hash)
	if err != nil {
		return err
	}
	zero := map[string]bool{}
	if len(m.Chunks) > 0 {
		z, _ := s.zeroHash(chunkLen(m.Size, 0))
		zero[z] = true
		zl, _ := s.zeroHash(chunkLen(m.Size, len(m.Chunks)-1))
		zero[zl] = true
	}
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := out.Truncate(m.Size); err != nil {
		out.Close()
		return err
	}
	err = parallel(len(m.Chunks), func(i int) error {
		c := m.Chunks[i]
		if zero[c] {
			return nil
		}
		b, err := s.Get(c)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return fmt.Errorf("chunk %s of %s is missing", c, hash)
			}
			return err
		}
		_, err = out.WriteAt(b, int64(i)*ChunkSize)
		return err
	})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
