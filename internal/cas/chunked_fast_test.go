package cas

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// sparse writes a file of size bytes with data only at the given regions.
func sparse(t *testing.T, path string, size int64, regions map[int64][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(size)
	for off, b := range regions {
		f.WriteAt(b, off)
	}
	f.Close()
}

func blocks(t *testing.T, path string) int64 {
	var st syscall.Stat_t
	syscall.Stat(path, &st)
	return st.Blocks * 512
}

func TestHolesAreNotReadAndRoundTripSparse(t *testing.T) {
	s, _ := Open(t.TempDir())
	dir := t.TempDir()
	page := bytes.Repeat([]byte{7}, 4096)
	p := filepath.Join(dir, "img")
	sparse(t, p, 64*ChunkSize, map[int64][]byte{3 * ChunkSize: page, 40*ChunkSize + 8192: page})
	h, newChunks, err := s.PutChunked(p)
	if err != nil {
		t.Fatal(err)
	}
	// two chunks with data plus the zero chunk
	if newChunks != 2 {
		t.Fatalf("stored %d new data chunks, want 2", newChunks)
	}
	out := filepath.Join(dir, "out")
	if err := s.GetChunkedTo(h, out); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(p)
	b, _ := os.ReadFile(out)
	if !bytes.Equal(a, b) {
		t.Fatal("round trip differs")
	}
	if used := blocks(t, out); used > 4*ChunkSize {
		t.Fatalf("a 64 MiB image with two data chunks restored using %d bytes on disk; zero chunks should be holes", used)
	}
}

func TestOverlayLaysDirtyPagesOverTheBase(t *testing.T) {
	s, _ := Open(t.TempDir())
	dir := t.TempDir()
	baseImg := make([]byte, 16*ChunkSize)
	rand.New(rand.NewSource(2)).Read(baseImg)
	bp := filepath.Join(dir, "base")
	os.WriteFile(bp, baseImg, 0o644)
	baseHash, _, err := s.PutChunked(bp)
	if err != nil {
		t.Fatal(err)
	}
	// a diff snapshot: sparse, full size, only two dirty pages written
	d1 := bytes.Repeat([]byte{1}, 4096)
	d2 := make([]byte, 4096) // a page dirtied to all zeros is still data
	dp := filepath.Join(dir, "diff")
	sparse(t, dp, int64(len(baseImg)), map[int64][]byte{5*ChunkSize + 4096: d1, 11 * ChunkSize: d2})
	h, newChunks, err := s.PutChunkedOverlay(dp, baseHash)
	if err != nil {
		t.Fatal(err)
	}
	if newChunks != 2 {
		t.Fatalf("overlay stored %d new chunks, want 2 (one per dirty chunk)", newChunks)
	}
	want := append([]byte(nil), baseImg...)
	copy(want[5*ChunkSize+4096:], d1)
	copy(want[11*ChunkSize:], d2)
	out := filepath.Join(dir, "out")
	s.GetChunkedTo(h, out)
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, want) {
		t.Fatal("overlay result is not base with the dirty pages replaced")
	}
	bm, _ := s.ReadManifest(baseHash)
	nm, _ := s.ReadManifest(h)
	for i := range bm.Chunks {
		same := bm.Chunks[i] == nm.Chunks[i]
		if (i == 5 || i == 11) == same {
			t.Fatalf("chunk %d: shared with base = %v", i, same)
		}
	}
}
