package fcvm

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildFakeFC compiles the stand-in firecracker binary once.
func buildFakeFC(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakefc")
	out, err := exec.Command("go", "build", "-o", bin, "./testdata/fakefc").CombinedOutput()
	if err != nil {
		t.Fatalf("building fakefc: %v\n%s", err, out)
	}
	return bin
}

func newDriver(t *testing.T) (*Firecracker, string) {
	t.Helper()
	run := t.TempDir()
	f, err := New(Config{FirecrackerBin: buildFakeFC(t), KernelImage: "/k/vmlinux",
		RootFS: "/k/rootfs.ext4", RunDir: run, VCPUs: 2, MemMiB: 256})
	if err != nil {
		t.Fatal(err)
	}
	return f, run
}

func calls(t *testing.T, run, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(run, id+".sock.calls"))
	if err != nil {
		t.Fatalf("no call log for %s: %v", id, err)
	}
	return string(b)
}

func TestBootSendsTheRightSequence(t *testing.T) {
	f, run := newDriver(t)
	if err := f.Boot(context.Background(), "vm1", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown(context.Background(), "vm1")
	log := calls(t, run, "vm1")
	for _, want := range []string{
		"PUT /boot-source", "kernel_image_path", "/k/vmlinux",
		"PUT /drives/rootfs", "is_root_device", "/k/rootfs.ext4",
		"PUT /machine-config", "vcpu_count", "mem_size_mib",
		"PUT /vsock", "guest_cid",
		"PUT /actions", "InstanceStart",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("boot did not send %q; calls were:\n%s", want, log)
		}
	}
}

func TestSnapshotPausesCreatesResumesAndWritesFiles(t *testing.T) {
	f, run := newDriver(t)
	ctx := context.Background()
	if err := f.Boot(ctx, "vm2", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown(ctx, "vm2")
	dir := t.TempDir()
	memPath, statePath, err := f.Snapshot(ctx, "vm2", dir)
	if err != nil {
		t.Fatal(err)
	}
	log := calls(t, run, "vm2")
	// order matters: pause, create, resume
	iPause := strings.Index(log, `PATCH /vm {"state":"Paused"}`)
	iCreate := strings.Index(log, "PUT /snapshot/create")
	iResume := strings.Index(log, `PATCH /vm {"state":"Resumed"}`)
	if iPause < 0 || iCreate < 0 || iResume < 0 || !(iPause < iCreate && iCreate < iResume) {
		t.Fatalf("snapshot order wrong:\n%s", log)
	}
	if b, _ := os.ReadFile(memPath); string(b) != "fake-memory-image" {
		t.Fatalf("memory file not written: %q", b)
	}
	if b, _ := os.ReadFile(statePath); string(b) != "fake-vm-state" {
		t.Fatalf("state file not written: %q", b)
	}
}

func TestRestoreLoadsSnapshot(t *testing.T) {
	f, run := newDriver(t)
	ctx := context.Background()
	f.Boot(ctx, "vm3", t.TempDir())
	dir := t.TempDir()
	mem, state, _ := f.Snapshot(ctx, "vm3", dir)
	if err := f.Fork(ctx, "vm3-fork", t.TempDir(), mem, state); err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown(ctx, "vm3")
	defer f.Shutdown(ctx, "vm3-fork")
	log := calls(t, run, "vm3-fork")
	if !strings.Contains(log, "PUT /snapshot/load") || !strings.Contains(log, `"resume_vm":true`) {
		t.Fatalf("fork did not load-and-resume the snapshot:\n%s", log)
	}
}
