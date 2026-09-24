package fcvm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/numinous-technology/vitvm/internal/engine"
	"github.com/numinous-technology/vitvm/internal/tree"
)

var fakeBin, vitBin string

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "fakefc")
	fakeBin = filepath.Join(dir, "fakefc")
	if out, err := exec.Command("go", "build", "-o", fakeBin, "./testdata/fakefc").CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		os.Exit(1)
	}
	vitBin = filepath.Join(dir, "vit") // the forwarder
	if out, err := exec.Command("go", "build", "-o", vitBin, "../../cmd/vit").CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func driver(t *testing.T, run string) *Firecracker {
	t.Helper()
	base := filepath.Join(t.TempDir(), "base.ext4")
	os.WriteFile(base, []byte("{}"), 0o644)
	f, err := New(Config{FirecrackerBin: fakeBin, KernelImage: "/k/vmlinux", RootFS: base, RunDir: run, VCPUs: 2, MemMiB: 256})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func sh(t *testing.T, f *Firecracker, id, script string) string {
	t.Helper()
	var out, errb bytes.Buffer
	code, err := f.Exec(context.Background(), id, "", []string{"sh", "-c", script}, nil, &out, &errb)
	if err != nil || code != 0 {
		t.Fatalf("exec %q in %s: code=%d err=%v stderr=%s", script, id, code, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

func calls(t *testing.T, f *Firecracker, id string) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(f.dir(id), "calls.log"))
	return string(b)
}

func TestBootUsesRelativeDevicePathsInTheVMDirectory(t *testing.T) {
	f := driver(t, t.TempDir())
	ctx := context.Background()
	if err := f.Boot(ctx, "vm1", ""); err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown(ctx, "vm1")
	log := calls(t, f, "vm1")
	for _, want := range []string{
		"cwd=" + f.dir("vm1") + " PUT /boot-source",
		// overlay mode: a shared read-only base and this VM's writable disk,
		// both by relative path, so a fork opens its own
		`"drive_id":"rootfs","is_read_only":true,"is_root_device":true,"path_on_host":"base.ext4"`,
		`"drive_id":"upper","is_read_only":false,"is_root_device":false,"path_on_host":"upper.ext4"`,
		`"uds_path":"v.sock"`, // relative: a fork gets its own socket
		`"vcpu_count":2`, `"mem_size_mib":256`,
		"PUT /actions", "InstanceStart",
	} {
		if !strings.Contains(log, want) {
			t.Fatalf("boot is missing %q:\n%s", want, log)
		}
	}
	if !f.Running(ctx, "vm1") {
		t.Fatal("booted VM should be running")
	}
	if blocks(filepath.Join(f.dir("vm1"), "upper.ext4")) > 64<<20 {
		t.Fatal("the 8 GiB writable disk should be sparse")
	}
}

func blocks(path string) int64 {
	var st syscall.Stat_t
	syscall.Stat(path, &st)
	return st.Blocks * 512
}

func TestCopyModeGivesEachVMAPrivateRootDisk(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base.ext4")
	os.WriteFile(base, []byte("{}"), 0o644)
	f, err := New(Config{FirecrackerBin: fakeBin, KernelImage: "/k", RootFS: base, RunDir: t.TempDir(), DiskMode: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := f.Boot(ctx, "c1", ""); err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown(ctx, "c1")
	if !strings.Contains(calls(t, f, "c1"), `"path_on_host":"rootfs.ext4"`) || strings.Contains(calls(t, f, "c1"), "upper.ext4") {
		t.Fatalf("copy mode should use one private root disk:\n%s", calls(t, f, "c1"))
	}
	img, err := f.Snapshot(ctx, "c1", t.TempDir(), "")
	if err != nil || img.BaseDisk != "" {
		t.Fatalf("copy mode has no base disk: %+v %v", img, err)
	}
}

func TestAVMOutlivesTheDriverThatStartedIt(t *testing.T) {
	run := t.TempDir()
	ctx := context.Background()
	first := driver(t, run)
	if err := first.Boot(ctx, "vm2", ""); err != nil {
		t.Fatal(err)
	}
	sh(t, first, "vm2", "echo persisted > note")
	// a separate vit command: a new driver with no memory of the first
	second := driver(t, run)
	defer second.Shutdown(ctx, "vm2")
	if !second.Running(ctx, "vm2") {
		t.Fatal("a new driver must find the running VM")
	}
	if got := sh(t, second, "vm2", "cat note"); got != "persisted" {
		t.Fatalf("second driver read %q", got)
	}
}

func TestGuestFiles(t *testing.T) {
	f := driver(t, t.TempDir())
	ctx := context.Background()
	f.Boot(ctx, "vm3", "")
	defer f.Shutdown(ctx, "vm3")
	blobs := map[string][]byte{"h1": []byte("alpha\n"), "h2": []byte("beta\n")}
	tr := &tree.Tree{Entries: tree.Sorted([]tree.Entry{
		{Path: "a.txt", Mode: 0o644, Hash: "h1"}, {Path: "d", Dir: true, Mode: 0o755},
		{Path: "d/b.txt", Mode: 0o600, Hash: "h2"}, {Path: "l", Link: "a.txt"},
	})}
	sh(t, f, "vm3", "echo stale > old.txt")
	err := f.WriteTree(ctx, "vm3", tr, func(h string) ([]byte, error) { return blobs[h], nil })
	if err != nil {
		t.Fatal(err)
	}
	entries, err := f.ListFiles(ctx, "vm3")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]tree.Entry{}
	for _, e := range entries {
		got[e.Path] = e
	}
	if _, stale := got["old.txt"]; stale {
		t.Fatal("WriteTree must replace the work tree, not add to it")
	}
	if got["a.txt"].Hash == "" || !got["d"].Dir || got["l"].Link != "a.txt" {
		t.Fatalf("listing: %+v", entries)
	}
	if b, _ := f.ReadFile(ctx, "vm3", "d/b.txt"); string(b) != "beta\n" {
		t.Fatalf("read %q", b)
	}
}

func TestSnapshotAndForkCarryMemoryAndDisk(t *testing.T) {
	f := driver(t, t.TempDir())
	ctx := context.Background()
	f.Boot(ctx, "orig", "")
	defer f.Shutdown(ctx, "orig")
	sh(t, f, "orig", "echo at-snapshot > f")
	nonce := sh(t, f, "orig", "cat ../ram")
	img, err := f.Snapshot(ctx, "orig", t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if img.MemoryIsDiff || !strings.Contains(calls(t, f, "orig"), `"snapshot_type":"Full"`) {
		t.Fatal("a snapshot with no confirmed base must be full")
	}
	img.Base = "img-1"
	log := calls(t, f, "orig")
	iP := strings.Index(log, `PATCH /vm {"state":"Paused"}`)
	iC := strings.Index(log, "PUT /snapshot/create")
	iR := strings.Index(log, `PATCH /vm {"state":"Resumed"}`)
	if !(iP >= 0 && iP < iC && iC < iR) {
		t.Fatalf("snapshot must pause, create, resume:\n%s", log)
	}
	sh(t, f, "orig", "echo after > f") // the original moves on
	if err := f.Resume(ctx, "fork", "", img); err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown(ctx, "fork")
	if got := sh(t, f, "fork", "cat ../ram"); got != nonce {
		t.Fatalf("fork memory %q, want the snapshot's %q", got, nonce)
	}
	if got := sh(t, f, "fork", "cat f"); got != "at-snapshot" {
		t.Fatalf("fork disk has f=%q, want the snapshot's", got)
	}
	if !strings.Contains(calls(t, f, "fork"), "cwd="+f.dir("fork")+" PUT /snapshot/load") {
		t.Fatal("the fork must load in its own directory")
	}
	if !strings.Contains(calls(t, f, "fork"), `"track_dirty_pages":true`) {
		t.Fatal("a resumed VM must track dirty pages so its next snapshot can be a diff")
	}
	// memory is loaded in place from the image, not copied
	if !strings.Contains(calls(t, f, "fork"), img.Memory) {
		t.Fatal("resume should map the image's memory file directly")
	}
	// the fork's base is the image it came from: a diff against it, a full
	// snapshot against anything else
	d1, _ := f.Snapshot(ctx, "fork", t.TempDir(), "img-1")
	d2, _ := f.Snapshot(ctx, "fork", t.TempDir(), "something-else")
	if !d1.MemoryIsDiff || d2.MemoryIsDiff {
		t.Fatalf("diff against the recorded base: %v, against another: %v", d1.MemoryIsDiff, d2.MemoryIsDiff)
	}
	sh(t, f, "fork", "echo fork > f")
	if got := sh(t, f, "orig", "cat f"); got != "after" {
		t.Fatalf("fork wrote into the original's disk: %q", got)
	}
}

func TestShutdownStopsAndCleansUp(t *testing.T) {
	f := driver(t, t.TempDir())
	ctx := context.Background()
	f.Boot(ctx, "vm4", "")
	p := f.pid("vm4")
	f.Shutdown(ctx, "vm4")
	if f.Running(ctx, "vm4") || alive(p) {
		t.Fatal("shutdown must stop the VM")
	}
	if _, err := os.Stat(f.dir("vm4")); !os.IsNotExist(err) {
		t.Fatal("shutdown must remove the VM directory")
	}
}

// The engine, end to end through this driver: steps, show, diff, warm fork,
// checkout, stop and resume.
func TestEngineThroughTheDriver(t *testing.T) {
	f := driver(t, t.TempDir())
	repo, _ := engine.OpenRepo(t.TempDir())
	e := engine.New(repo, f)
	ctx := context.Background()
	s, _ := e.Create("vm")
	defer f.Shutdown(ctx, s.ID)
	run := func(sb *engine.Sandbox, script string) (*engine.Checkpoint, string) {
		var out bytes.Buffer
		c, err := e.Run(ctx, sb, []string{"sh", "-c", script}, &out, io.Discard)
		if err != nil {
			t.Fatalf("run %q: %v", script, err)
		}
		return c, strings.TrimSpace(out.String())
	}
	c1, _ := run(s, "echo one > notes.txt")
	_, nonce := run(s, "cat ../ram")
	if !c1.HasMemory() || c1.DiskHash == "" || c1.BaseHash == "" {
		t.Fatalf("firecracker checkpoints carry memory, the writable disk and the base: %+v", c1)
	}
	if b, _ := e.ReadFile(c1.ID, "notes.txt"); string(b) != "one\n" {
		t.Fatalf("show: %q", b)
	}
	c3, _ := run(s, "echo two >> notes.txt")
	if !strings.Contains(calls(t, f, s.ID), `"snapshot_type":"Diff"`) {
		t.Fatal("steps after the first should take diff snapshots")
	}
	if ch, _ := e.Diff(c1.ID, c3.ID); len(ch) != 1 || ch[0].Kind != "modified" {
		t.Fatalf("diff: %+v", ch)
	}
	fork, err := e.Fork(c1.ID, "branch")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Shutdown(ctx, fork.ID)
	if _, got := run(fork, "cat ../ram; echo; cat notes.txt"); got != nonce+"\none" {
		t.Fatalf("warm fork: %q, want nonce %s and step 1's notes", got, nonce)
	}
	if err := e.Stop(ctx, s); err != nil {
		t.Fatal(err)
	}
	if _, got := run(s, "cat ../ram; echo; cat notes.txt"); got != nonce+"\none\ntwo" {
		t.Fatalf("after stop, run resumed %q", got)
	}
}

// A stand-in gmux host: greets, then echoes a line.
func gmuxHost(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write([]byte("gpu-host\n"))
				line, _ := bufio.NewReader(c).ReadString('\n')
				c.Write([]byte("echo " + line))
			}(c)
		}
	}()
	return ln.Addr().String()
}

func freePort(t *testing.T) int {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestAMachineReachesItsGmuxHostsThroughTheTunnel(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base.ext4")
	os.WriteFile(base, []byte("{}"), 0o644)
	gport := freePort(t)
	f, err := New(Config{FirecrackerBin: fakeBin, KernelImage: "/k", RootFS: base, RunDir: t.TempDir(),
		Forwards:      []Forward{{Name: "gpu1", Target: gmuxHost(t), Token: "tok", Fingerprint: "ff"}},
		GuestPortBase: gport, ForwarderBin: vitBin})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := f.Boot(ctx, "g1", ""); err != nil {
		t.Fatal(err)
	}
	pids, _ := os.ReadFile(filepath.Join(f.dir("g1"), "forward.pids"))
	// guest command -> guest port -> vsock -> forwarder -> the gmux host
	script := fmt.Sprintf(`import socket
s=socket.create_connection(("127.0.0.1",%d)); f=s.makefile("rw")
print(f.readline().strip()); f.write("ping\n"); f.flush(); print(f.readline().strip())`, gport)
	var out, errb bytes.Buffer
	if code, err := f.Exec(ctx, "g1", "", []string{"python3", "-c", script}, nil, &out, &errb); err != nil || code != 0 {
		t.Fatalf("exec: %d %v %s", code, err, errb.String())
	}
	if out.String() != "gpu-host\necho ping\n" {
		t.Fatalf("through the tunnel: %q", out.String())
	}
	// the sandbox's gmux settings
	out.Reset()
	f.Exec(ctx, "g1", "", []string{"sh", "-c", `echo "$GMUX_SESSION_PREFIX|$GMUX_OWNER|$GMUX_NAME"; cat "$GMUX_CONFIG"`}, []string{"VIT_STEP=3"}, &out, &errb)
	lines := strings.SplitN(out.String(), "\n", 2)
	if lines[0] != "g1|vit-g1|vit-g1-step3" {
		t.Fatalf("gmux env: %q", lines[0])
	}
	if !strings.Contains(lines[1], fmt.Sprintf(`"gpu1":"tok@127.0.0.1:%d#ff"`, gport)) || !strings.Contains(lines[1], `"default":"gpu1"`) {
		t.Fatalf("guest gmux config: %q", lines[1])
	}
	if _, err := f.InFlight(ctx, "g1"); err != nil {
		t.Fatal(err)
	}
	f.Shutdown(ctx, "g1")
	for _, p := range strings.Fields(string(pids)) {
		n, _ := strconv.Atoi(p)
		if alive(n) {
			t.Fatal("the forwarder must stop with the machine")
		}
	}
}
