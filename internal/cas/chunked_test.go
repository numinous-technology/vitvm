package cas

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChunkedRoundTripAndDedup(t *testing.T) {
	s, _ := Open(t.TempDir())
	dir := t.TempDir()
	// an 8.5 MiB "memory image": random data, with a zeroed region
	img := make([]byte, 8*ChunkSize+ChunkSize/2)
	rand.New(rand.NewSource(1)).Read(img)
	for i := 2 * ChunkSize; i < 5*ChunkSize; i++ {
		img[i] = 0
	}
	a := filepath.Join(dir, "a")
	writeFile(t, a, img)
	h1, new1, err := s.PutChunked(a)
	if err != nil {
		t.Fatal(err)
	}
	// 9 chunks, but the three zero chunks are one blob
	if new1 != 7 {
		t.Fatalf("first image stored %d new chunks, want 7 (9 chunks, 3 identical zero chunks)", new1)
	}
	out := filepath.Join(dir, "out")
	if err := s.GetChunkedTo(h1, out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, img) {
		t.Fatal("reassembled image differs")
	}
	// the next step changes one page: one new chunk
	img[6*ChunkSize+4096] ^= 0xff
	b := filepath.Join(dir, "b")
	writeFile(t, b, img)
	h2, new2, _ := s.PutChunked(b)
	if new2 != 1 || h2 == h1 {
		t.Fatalf("one changed page stored %d new chunks (want 1), same manifest=%v", new2, h2 == h1)
	}
	s.GetChunkedTo(h2, out)
	if got, _ := os.ReadFile(out); !bytes.Equal(got, img) {
		t.Fatal("second image differs")
	}
}

func TestChunkedEmptyFileAndNotAManifest(t *testing.T) {
	s, _ := Open(t.TempDir())
	p := filepath.Join(t.TempDir(), "empty")
	writeFile(t, p, nil)
	h, _, err := s.PutChunked(p)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "o")
	if err := s.GetChunkedTo(h, out); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); fi.Size() != 0 {
		t.Fatal("empty file should round trip empty")
	}
	plain, _ := s.Put([]byte("not json"))
	if _, err := s.ReadManifest(plain); err == nil {
		t.Fatal("a plain blob is not a manifest")
	}
}
