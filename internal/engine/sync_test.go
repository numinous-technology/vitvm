package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/numinous-technology/vitvm/internal/remote"
)

func buildSandbox(t *testing.T) (*Engine, *Sandbox, string) {
	t.Helper()
	repo, err := OpenRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := New(repo, ProcessBackend{})
	s, _ := e.Create("demo")
	run := func(args ...string) *Checkpoint {
		c, err := e.Run(context.Background(), s, args, io.Discard, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	run("sh", "-c", "echo hello > greeting.txt")
	c2 := run("sh", "-c", "echo more >> greeting.txt; echo x > extra.txt")
	return e, s, c2.ID
}

// pushPullFork pushes a sandbox to store, pulls its head checkpoint into a
// fresh repo, forks it, and checks the files came through. It is backend
// agnostic so it runs against both the directory store and the S3 mock.
func pushPullFork(t *testing.T, store remote.ObjectStore) {
	t.Helper()
	e, s, head := buildSandbox(t)
	st, err := e.Push(s.ID, store)
	if err != nil {
		t.Fatal(err)
	}
	if st.Checkpoints < 2 || st.Blobs == 0 {
		t.Fatalf("push moved too little: %+v", st)
	}
	// a second push uploads no new blobs
	st2, _ := e.Push(s.ID, store)
	if st2.Blobs != 0 {
		t.Fatalf("re-push should upload 0 blobs, got %d", st2.Blobs)
	}

	// fresh repo on a "different machine"
	repo2, _ := OpenRepo(t.TempDir())
	e2 := New(repo2, ProcessBackend{})
	c, err := e2.Pull(store, head)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	// read a file straight from the pulled checkpoint, no run
	body, err := e2.ReadFile(c.ID, "greeting.txt")
	if err != nil || string(body) != "hello\nmore\n" {
		t.Fatalf("pulled file wrong: %q %v", body, err)
	}
	// fork it and confirm the working tree materialises
	fork, err := e2.Fork(c.ID, "restored")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(e2.Repo().WorkDir(fork.ID), "extra.txt")); string(b) != "x\n" {
		t.Fatalf("forked workspace missing file: %q", b)
	}
}

func TestPushPullForkDirStore(t *testing.T) {
	store, err := remote.NewDirStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pushPullFork(t, store)
}

func TestPullMissingCheckpoint(t *testing.T) {
	store, _ := remote.NewDirStore(t.TempDir())
	repo, _ := OpenRepo(t.TempDir())
	e := New(repo, ProcessBackend{})
	if _, err := e.Pull(store, "ck-nope"); err == nil {
		t.Fatal("pulling an absent checkpoint should error")
	}
}
