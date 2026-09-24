package engine

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func newEngine(t *testing.T) *Engine {
	t.Helper()
	repo, err := OpenRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(repo, ProcessBackend{})
}

func runStep(t *testing.T, e *Engine, s *Sandbox, args ...string) *Checkpoint {
	t.Helper()
	c, err := e.Run(context.Background(), s, args, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return c
}

func TestRunCheckpointsEveryStep(t *testing.T) {
	e := newEngine(t)
	s, err := e.Create("demo")
	if err != nil {
		t.Fatal(err)
	}
	c1 := runStep(t, e, s, "sh", "-c", "echo one > a.txt")
	c2 := runStep(t, e, s, "sh", "-c", "echo two > b.txt")

	if c1.Added != 1 || c2.Added != 1 {
		t.Fatalf("each step should add one file: %+v %+v", c1, c2)
	}
	hist, _ := e.Repo().History(s.ID)
	// c2, c1, and the initial empty checkpoint
	if len(hist) != 3 {
		t.Fatalf("want 3 checkpoints, got %d", len(hist))
	}

	// read a past step without re-running
	got, err := e.ReadFile(c1.ID, "a.txt")
	if err != nil || string(got) != "one\n" {
		t.Fatalf("read past checkpoint: %q %v", got, err)
	}
	// b.txt did not exist at c1
	if _, err := e.ReadFile(c1.ID, "b.txt"); err == nil {
		t.Fatal("b.txt should not exist at c1")
	}
}

func TestCheckoutRewindsThenDiverges(t *testing.T) {
	e := newEngine(t)
	s, _ := e.Create("demo")
	c1 := runStep(t, e, s, "sh", "-c", "echo v1 > f.txt")
	runStep(t, e, s, "sh", "-c", "echo v2 > f.txt")

	// working dir now has v2
	if err := e.Checkout(s, c1.ID); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(e.Repo().WorkDir(s.ID), "f.txt"))
	if string(body) != "v1\n" {
		t.Fatalf("checkout should restore v1, got %q", body)
	}
}

func TestForkSharesHistoryPointButDiverges(t *testing.T) {
	e := newEngine(t)
	s, _ := e.Create("parent")
	c1 := runStep(t, e, s, "sh", "-c", "echo base > shared.txt")
	runStep(t, e, s, "sh", "-c", "echo parent-only > p.txt")

	fork, err := e.Fork(c1.ID, "child")
	if err != nil {
		t.Fatal(err)
	}
	if fork.ForkedFrom != c1.ID {
		t.Fatal("fork should record its origin")
	}
	// child sees the base file but not the parent's later work
	if b, _ := os.ReadFile(filepath.Join(e.Repo().WorkDir(fork.ID), "shared.txt")); string(b) != "base\n" {
		t.Fatalf("child should have shared.txt: %q", b)
	}
	if _, err := os.Stat(filepath.Join(e.Repo().WorkDir(fork.ID), "p.txt")); !os.IsNotExist(err) {
		t.Fatal("child must not have the parent's later file")
	}
	// child diverges without touching the parent
	runStep(t, e, fork, "sh", "-c", "echo child-only > c.txt")
	if _, err := os.Stat(filepath.Join(e.Repo().WorkDir(s.ID), "c.txt")); !os.IsNotExist(err) {
		t.Fatal("parent must not see the child's work")
	}
}

func TestDiffBetweenCheckpoints(t *testing.T) {
	e := newEngine(t)
	s, _ := e.Create("demo")
	c1 := runStep(t, e, s, "sh", "-c", "echo a > a.txt; echo b > b.txt")
	c2 := runStep(t, e, s, "sh", "-c", "echo changed > a.txt; rm b.txt; echo c > c.txt")

	changes, err := e.Diff(c1.ID, c2.ID)
	if err != nil {
		t.Fatal(err)
	}
	kind := map[string]string{}
	for _, ch := range changes {
		kind[ch.Path] = ch.Kind
	}
	if kind["a.txt"] != "modified" || kind["b.txt"] != "deleted" || kind["c.txt"] != "added" {
		t.Fatalf("unexpected diff: %v", kind)
	}
}
