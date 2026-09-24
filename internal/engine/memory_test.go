package engine

import (
	"context"
	"io"
	"testing"
)

func newMemEngine(t *testing.T) (*Engine, *FakeMemoryBackend) {
	t.Helper()
	repo, err := OpenRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fb := NewFakeMemoryBackend()
	return New(repo, fb), fb
}

func TestCheckpointCapturesMemory(t *testing.T) {
	e, fb := newMemEngine(t)
	s, _ := e.Create("vm")
	// each step mutates memory so successive checkpoints differ
	fb.Touch(s.ID, []byte("A"))
	c1, err := e.Run(context.Background(), s, []string{"sh", "-c", "echo one > f.txt"}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	fb.Touch(s.ID, []byte("B"))
	c2, _ := e.Run(context.Background(), s, []string{"sh", "-c", "echo two >> f.txt"}, io.Discard, io.Discard)

	if !c1.HasMemory() || !c2.HasMemory() {
		t.Fatal("a memory backend's checkpoints must carry a machine image")
	}
	if c1.MemHash == c2.MemHash {
		t.Fatal("memory changed between steps, so the hashes must differ")
	}
	// the created checkpoint (before any run) has no memory
	hist, _ := e.Repo().History(s.ID)
	root := hist[len(hist)-1]
	if root.HasMemory() {
		t.Fatal("the initial checkpoint should not carry memory")
	}
}

func TestForkResumesMemoryOfThatStep(t *testing.T) {
	e, fb := newMemEngine(t)
	s, _ := e.Create("vm")
	fb.Touch(s.ID, []byte("step1"))
	c1, _ := e.Run(context.Background(), s, []string{"sh", "-c", "echo a > a.txt"}, io.Discard, io.Discard)
	mem1 := fb.Memory(s.ID) // memory as captured at c1
	fb.Touch(s.ID, []byte("step2"))
	e.Run(context.Background(), s, []string{"sh", "-c", "echo b > b.txt"}, io.Discard, io.Discard)

	// fork from c1: the new machine's memory must equal c1's, not the later state
	fork, err := e.Fork(c1.ID, "branch")
	if err != nil {
		t.Fatal(err)
	}
	got := fb.Memory(fork.ID)
	if string(got) != string(mem1) {
		t.Fatalf("fork did not resume the step's memory:\n got  %q\n want %q", got, mem1)
	}
}

func TestCheckoutRestoresMemory(t *testing.T) {
	e, fb := newMemEngine(t)
	s, _ := e.Create("vm")
	fb.Touch(s.ID, []byte("v1"))
	c1, _ := e.Run(context.Background(), s, []string{"sh", "-c", "echo v1 > f.txt"}, io.Discard, io.Discard)
	mem1 := fb.Memory(s.ID)
	fb.Touch(s.ID, []byte("v2"))
	e.Run(context.Background(), s, []string{"sh", "-c", "echo v2 > f.txt"}, io.Discard, io.Discard)

	if err := e.Checkout(s, c1.ID); err != nil {
		t.Fatal(err)
	}
	if string(fb.Memory(s.ID)) != string(mem1) {
		t.Fatal("checkout should have restored the machine memory to c1")
	}
}
