package engine

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/numinous-technology/vitvm/internal/remote"
)

func machineEngine(t *testing.T) (*Engine, *FakeMachine) {
	t.Helper()
	repo, err := OpenRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fm := NewFakeMachine(t.TempDir())
	return New(repo, fm), fm
}

func step(t *testing.T, e *Engine, s *Sandbox, script string) *Checkpoint {
	t.Helper()
	c, err := e.Run(context.Background(), s, []string{"sh", "-c", script}, io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("run %q: %v", script, err)
	}
	return c
}

func guestFile(t *testing.T, fm *FakeMachine, id, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fm.GuestDir(id), rel))
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestMachineCheckpointCarriesGuestFilesAndImage(t *testing.T) {
	e, _ := machineEngine(t)
	s, _ := e.Create("vm")
	c := step(t, e, s, "echo hello > note.txt; mkdir -p src; echo x > src/a.py")
	if !c.HasMemory() || c.DiskHash == "" {
		t.Fatalf("a machine step must carry memory, state and disk: %+v", c)
	}
	// the files came from the guest; the host working directory is untouched
	if entries, _ := os.ReadDir(e.Repo().WorkDir(s.ID)); len(entries) != 0 {
		t.Fatal("machine files must not appear in the host working directory")
	}
	if got, err := e.ReadFile(c.ID, "note.txt"); err != nil || string(got) != "hello\n" {
		t.Fatalf("show from a machine checkpoint: %q %v", got, err)
	}
	c2 := step(t, e, s, "echo changed > note.txt; rm src/a.py")
	changes, _ := e.Diff(c.ID, c2.ID)
	kind := map[string]string{}
	for _, ch := range changes {
		kind[ch.Path] = ch.Kind
	}
	if kind["note.txt"] != "modified" || kind["src/a.py"] != "deleted" {
		t.Fatalf("diff between machine checkpoints: %v", kind)
	}
}

func TestWarmForkResumesThatStepsMemoryAndDisk(t *testing.T) {
	e, fm := machineEngine(t)
	s, _ := e.Create("vm")
	fm.Boot(context.Background(), s.ID, "")
	fm.Touch(s.ID, []byte("step1;"))
	c1 := step(t, e, s, "echo 1 > f")
	mem1 := fm.Memory(s.ID)
	fm.Touch(s.ID, []byte("step2;"))
	step(t, e, s, "echo 2 > f")

	fork, err := e.Fork(c1.ID, "branch")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fm.Memory(fork.ID), mem1) {
		t.Fatalf("fork memory = %q, want step 1's %q", fm.Memory(fork.ID), mem1)
	}
	if got := guestFile(t, fm, fork.ID, "f"); got != "1\n" {
		t.Fatalf("fork disk has f=%q, want step 1's", got)
	}
	if got := guestFile(t, fm, s.ID, "f"); got != "2\n" {
		t.Fatalf("parent must be untouched, has f=%q", got)
	}
	// the fork diverges on its own machine
	step(t, e, fork, "echo fork > f")
	if guestFile(t, fm, s.ID, "f") != "2\n" {
		t.Fatal("fork's step leaked into the parent")
	}
}

func TestCheckoutResumesTheImage(t *testing.T) {
	e, fm := machineEngine(t)
	s, _ := e.Create("vm")
	fm.Boot(context.Background(), s.ID, "")
	fm.Touch(s.ID, []byte("v1;"))
	c1 := step(t, e, s, "echo v1 > f")
	mem1 := fm.Memory(s.ID)
	fm.Touch(s.ID, []byte("v2;"))
	step(t, e, s, "echo v2 > f")
	if err := e.Checkout(s, c1.ID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fm.Memory(s.ID), mem1) || guestFile(t, fm, s.ID, "f") != "v1\n" {
		t.Fatalf("checkout did not resume step 1: mem=%q f=%q", fm.Memory(s.ID), guestFile(t, fm, s.ID, "f"))
	}
}

func TestColdForkFromAFilesOnlyCheckpoint(t *testing.T) {
	// a checkpoint made by the process backend has files but no image
	repo, _ := OpenRepo(t.TempDir())
	pe := New(repo, ProcessBackend{})
	ps, _ := pe.Create("host")
	c := step(t, pe, ps, "mkdir -p data; echo from-host > data/x")
	if c.HasMemory() {
		t.Fatal("process checkpoints carry no image")
	}
	fm := NewFakeMachine(t.TempDir())
	me := New(repo, fm)
	fork, err := me.Fork(c.ID, "vm")
	if err != nil {
		t.Fatal(err)
	}
	if !fm.Running(context.Background(), fork.ID) {
		t.Fatal("a cold fork on a machine backend boots a machine")
	}
	if got := guestFile(t, fm, fork.ID, "data/x"); got != "from-host\n" {
		t.Fatalf("cold fork guest has data/x=%q", got)
	}
}

func TestStoppedSandboxComesBackFromItsHead(t *testing.T) {
	e, fm := machineEngine(t)
	s, _ := e.Create("vm")
	fm.Boot(context.Background(), s.ID, "")
	fm.Touch(s.ID, []byte("warm;"))
	step(t, e, s, "echo kept > f")
	mem := fm.Memory(s.ID)
	if err := e.Stop(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if fm.Running(context.Background(), s.ID) {
		t.Fatal("stop must shut the machine down")
	}
	var out bytes.Buffer
	if _, err := e.Run(context.Background(), s, []string{"cat", "f"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "kept" {
		t.Fatalf("after stop, run saw f=%q", out.String())
	}
	if !bytes.HasPrefix(fm.Memory(s.ID), mem) {
		t.Fatalf("the machine should have resumed its checkpointed memory, has %q", fm.Memory(s.ID))
	}
}

func TestPushPullAndWarmForkOfMachineCheckpoints(t *testing.T) {
	e, fm := machineEngine(t)
	s, _ := e.Create("vm")
	fm.Boot(context.Background(), s.ID, "")
	fm.Touch(s.ID, []byte("remote-state;"))
	c := step(t, e, s, "echo shipped > f")
	mem := fm.Memory(s.ID)
	store, _ := remote.NewDirStore(t.TempDir())
	if _, err := e.Push(s.ID, store); err != nil {
		t.Fatal(err)
	}
	// another machine: fresh repo, fresh backend
	repo2, _ := OpenRepo(t.TempDir())
	fm2 := NewFakeMachine(t.TempDir())
	e2 := New(repo2, fm2)
	pulled, err := e2.Pull(store, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !pulled.HasMemory() || pulled.DiskHash == "" {
		t.Fatal("pull must bring the machine image")
	}
	fork, err := e2.Fork(pulled.ID, "there")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fm2.Memory(fork.ID), mem) || guestFile(t, fm2, fork.ID, "f") != "shipped\n" {
		t.Fatalf("warm fork after pull: mem=%q f=%q", fm2.Memory(fork.ID), guestFile(t, fm2, fork.ID, "f"))
	}
}
