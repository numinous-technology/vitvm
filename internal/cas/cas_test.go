package cas

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPutIsContentAddressedAndDeduplicates(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := s.Put([]byte("hello"))
	h2, _ := s.Put([]byte("hello"))
	h3, _ := s.Put([]byte("world"))
	if h1 != h2 {
		t.Fatal("same bytes, same hash")
	}
	if h1 == h3 {
		t.Fatal("different bytes, different hash")
	}
	b, _ := s.Get(h1)
	if string(b) != "hello" {
		t.Fatalf("got %q", b)
	}
	if !s.Has(h1) || s.Has("deadbeef") {
		t.Fatal("has")
	}
	if _, err := s.Get("00ff"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestPutFileMatchesPut(t *testing.T) {
	s, _ := Open(t.TempDir())
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte("some content"), 0o644)
	hf, n, err := s.PutFile(p)
	if err != nil || n != 12 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	hb, _ := s.Put([]byte("some content"))
	if hf != hb {
		t.Fatal("PutFile and Put must agree on the hash")
	}
}
