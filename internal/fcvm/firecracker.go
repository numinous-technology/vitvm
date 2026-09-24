// Package fcvm is vitvm's Firecracker backend: each sandbox is a Firecracker
// microVM, and each checkpoint snapshots the guest's memory, device state and
// root disk at one instant, so a checkout or a fork resumes a live machine.
// It implements engine.MemoryBackend and engine.GuestFS.
//
// Each VM lives in its own directory under RunDir:
//
//	<RunDir>/<sandbox>/api.sock    Firecracker's API socket
//	<RunDir>/<sandbox>/v.sock      the vsock socket to the guest agent
//	<RunDir>/<sandbox>/rootfs.ext4 the VM's own disk
//	<RunDir>/<sandbox>/mem         the memory file a resumed VM maps
//	<RunDir>/<sandbox>/pid, fc.log
//
// Firecracker runs detached, so a VM outlives the vit command that started it,
// and the driver keeps no state of its own: any later command finds the VM
// from its directory. The disk and vsock paths given to Firecracker are
// relative and each Firecracker runs in its VM's directory, so a snapshot
// records "rootfs.ext4" and "v.sock", and a fork resumed in its own directory
// opens its own disk and socket rather than the original's.
//
// The driver speaks Firecracker's REST API with the standard library. It needs
// Linux with /dev/kvm, the firecracker binary, a guest kernel, and a root
// filesystem that runs vit-guest (scripts/build-rootfs.sh builds one).
package fcvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/numinous-technology/vitvm/internal/engine"
)

// Config points the driver at its host tools and guest images.
type Config struct {
	FirecrackerBin string // path to the firecracker binary
	KernelImage    string // uncompressed guest kernel (vmlinux)
	RootFS         string // base root filesystem image each new VM copies
	RunDir         string // where per-VM directories live
	VCPUs          int    // guest vCPUs (default 1)
	MemMiB         int    // guest memory in MiB (default 512)
	BootArgs       string // kernel command line
	AgentTimeout   time.Duration
	// DiskMode is "overlay" (default): the root filesystem is a shared
	// read-only base with a small sparse writable disk layered over it in the
	// guest, and only the writable disk is captured each step. "copy" gives
	// each VM a full private copy of the root filesystem, for guest kernels
	// without overlayfs.
	DiskMode string
	UpperGiB int // size of the sparse writable disk in overlay mode (default 8)
}

// Firecracker is a MemoryBackend and GuestFS backed by real microVMs.
type Firecracker struct{ cfg Config }

// DefaultBootArgs start the vit init in the guest.
const DefaultBootArgs = "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/vit-init"

// New builds the driver.
func New(cfg Config) (*Firecracker, error) {
	if cfg.FirecrackerBin == "" {
		cfg.FirecrackerBin = "firecracker"
	}
	if cfg.VCPUs == 0 {
		cfg.VCPUs = 1
	}
	if cfg.MemMiB == 0 {
		cfg.MemMiB = 512
	}
	if cfg.BootArgs == "" {
		cfg.BootArgs = DefaultBootArgs
	}
	if cfg.AgentTimeout == 0 {
		cfg.AgentTimeout = 60 * time.Second
	}
	if cfg.RunDir == "" {
		cfg.RunDir = filepath.Join(os.TempDir(), "vit-fc")
	}
	if cfg.DiskMode == "" {
		cfg.DiskMode = "overlay"
	}
	if cfg.UpperGiB == 0 {
		cfg.UpperGiB = 8
	}
	abs, err := filepath.Abs(cfg.RunDir)
	if err != nil {
		return nil, err
	}
	cfg.RunDir = abs
	if err := os.MkdirAll(cfg.RunDir, 0o755); err != nil {
		return nil, err
	}
	return &Firecracker{cfg: cfg}, nil
}

// Name identifies the backend.
func (f *Firecracker) Name() string { return "firecracker" }

func (f *Firecracker) dir(id string) string     { return filepath.Join(f.cfg.RunDir, id) }
func (f *Firecracker) apiSock(id string) string { return filepath.Join(f.dir(id), "api.sock") }
func (f *Firecracker) vsock(id string) string   { return filepath.Join(f.dir(id), "v.sock") }

// LogPath is the file holding a VM's console and Firecracker log.
func (f *Firecracker) LogPath(id string) string { return filepath.Join(f.dir(id), "fc.log") }

func (f *Firecracker) pid(id string) int {
	b, err := os.ReadFile(filepath.Join(f.dir(id), "pid"))
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// Running reports whether the sandbox's Firecracker process is alive.
func (f *Firecracker) Running(ctx context.Context, id string) bool {
	p := f.pid(id)
	if !alive(p) {
		return false
	}
	// a recycled pid is not our VM
	cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", p))
	return bytes.Contains(cmdline, []byte(f.apiSock(id)))
}

// alive reports whether pid is a live process (not gone, not a zombie).
func alive(pid int) bool {
	if pid <= 0 || syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return true
}

func client(sock string) *http.Client {
	return &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}}
}

// api makes one Firecracker REST call on the sandbox's API socket.
func (f *Firecracker) api(id, method, path string, body any) error {
	var r io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://localhost"+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client(f.apiSock(id)).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("firecracker %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// spawn starts a detached firecracker in the sandbox's directory and waits for
// its API socket.
func (f *Firecracker) spawn(id string) error {
	d := f.dir(id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	os.Remove(f.apiSock(id))
	os.Remove(f.vsock(id))
	logf, err := os.OpenFile(f.LogPath(id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(f.cfg.FirecrackerBin, "--api-sock", f.apiSock(id), "--id", strings.ReplaceAll(id, "_", "-"))
	cmd.Dir = d
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting firecracker: %w", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Release() // detached: it outlives this process
	if err := os.WriteFile(filepath.Join(d, "pid"), []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return err
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if c, err := net.Dial("unix", f.apiSock(id)); err == nil {
			c.Close()
			return nil
		}
	}
	syscall.Kill(pid, syscall.SIGKILL)
	return fmt.Errorf("firecracker API socket never came up for %s (see %s)", id, f.LogPath(id))
}

// upperTemplate returns an empty, sparse ext4 image of UpperGiB, made once
// per size and copied for each VM. Lazy inode-table and journal init keep it
// sparse; the guest mounts it with noinit_itable so it stays that way.
func (f *Firecracker) upperTemplate() (string, error) {
	p := filepath.Join(f.cfg.RunDir, fmt.Sprintf("upper-%dg.ext4", f.cfg.UpperGiB))
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	tmp, err := os.CreateTemp(f.cfg.RunDir, ".upper-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	if err := os.Truncate(tmp.Name(), int64(f.cfg.UpperGiB)<<30); err != nil {
		return "", err
	}
	out, err := exec.Command("mkfs.ext4", "-q", "-F", "-L", "vit-upper",
		"-E", "lazy_itable_init=1,lazy_journal_init=1", tmp.Name()).CombinedOutput()
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("making the writable disk: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return p, os.Rename(tmp.Name(), p)
}

// copyFile copies src to dst, sharing blocks with a reflink where the
// filesystem supports it and keeping holes sparse.
func copyFile(src, dst string) error {
	out, err := exec.Command("cp", "--reflink=auto", "--sparse=always", src, dst).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copying %s: %v: %s", src, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Boot starts a fresh VM on a copy of the base root filesystem.
func (f *Firecracker) Boot(ctx context.Context, id, workDir string) error {
	if f.Running(ctx, id) {
		return nil
	}
	if f.cfg.KernelImage == "" || f.cfg.RootFS == "" {
		return errors.New("firecracker backend needs a kernel and a root filesystem (vit config set firecracker.kernel / firecracker.rootfs)")
	}
	d := f.dir(id)
	os.RemoveAll(d)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	overlay := f.cfg.DiskMode == "overlay"
	if overlay {
		base, err := filepath.Abs(f.cfg.RootFS)
		if err != nil {
			return err
		}
		if err := os.Symlink(base, filepath.Join(d, "base.ext4")); err != nil {
			return err
		}
		tmpl, err := f.upperTemplate()
		if err != nil {
			return err
		}
		if err := copyFile(tmpl, filepath.Join(d, "upper.ext4")); err != nil {
			return err
		}
	} else if err := copyFile(f.cfg.RootFS, filepath.Join(d, "rootfs.ext4")); err != nil {
		return err
	}
	if err := f.spawn(id); err != nil {
		return err
	}
	type call struct {
		method, path string
		body         any
	}
	steps := []call{{"PUT", "/boot-source", map[string]any{"kernel_image_path": f.cfg.KernelImage, "boot_args": f.cfg.BootArgs}}}
	if overlay {
		steps = append(steps,
			call{"PUT", "/drives/rootfs", map[string]any{"drive_id": "rootfs", "path_on_host": "base.ext4", "is_root_device": true, "is_read_only": true}},
			call{"PUT", "/drives/upper", map[string]any{"drive_id": "upper", "path_on_host": "upper.ext4", "is_root_device": false, "is_read_only": false}})
	} else {
		steps = append(steps, call{"PUT", "/drives/rootfs", map[string]any{"drive_id": "rootfs", "path_on_host": "rootfs.ext4", "is_root_device": true, "is_read_only": false}})
	}
	steps = append(steps, []call{
		{"PUT", "/machine-config", map[string]any{"vcpu_count": f.cfg.VCPUs, "mem_size_mib": f.cfg.MemMiB, "track_dirty_pages": true}},
		{"PUT", "/vsock", map[string]any{"guest_cid": 3, "uds_path": "v.sock"}},
		{"PUT", "/actions", map[string]any{"action_type": "InstanceStart"}},
	}...)
	for _, s := range steps {
		if err := f.api(id, s.method, s.path, s.body); err != nil {
			f.Shutdown(ctx, id)
			return err
		}
	}
	return f.waitAgent(id)
}

func (f *Firecracker) baseFile(id string) string { return filepath.Join(f.dir(id), "base") }

// Rebase records the stored image the VM's memory now equals, so the next
// snapshot can be a diff against it.
func (f *Firecracker) Rebase(ctx context.Context, id, base string) error {
	return os.WriteFile(f.baseFile(id), []byte(base), 0o644)
}

// Snapshot pauses the VM, writes a snapshot and a copy of its disk taken while
// paused, and resumes it. When base is the image this VM was last resumed from
// or rebased onto, the memory is a diff: Firecracker writes only the pages
// dirtied since, as a sparse file. Otherwise it writes all of memory.
func (f *Firecracker) Snapshot(ctx context.Context, id, dir, base string) (img engine.Image, err error) {
	if !f.Running(ctx, id) {
		return img, fmt.Errorf("sandbox %s is not running", id)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return img, err
	}
	img = engine.Image{Memory: filepath.Join(absDir, "mem"), State: filepath.Join(absDir, "state"), Disk: filepath.Join(absDir, "disk")}
	recorded, _ := os.ReadFile(f.baseFile(id))
	kind := "Full"
	if base != "" && string(recorded) == base {
		kind, img.MemoryIsDiff, img.Base = "Diff", true, base
	}
	if err := f.api(id, "PATCH", "/vm", map[string]any{"state": "Paused"}); err != nil {
		return img, err
	}
	defer func() {
		if rerr := f.api(id, "PATCH", "/vm", map[string]any{"state": "Resumed"}); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if err := f.api(id, "PUT", "/snapshot/create", map[string]any{
		"snapshot_type": kind, "snapshot_path": img.State, "mem_file_path": img.Memory,
	}); err != nil {
		return img, err
	}
	writable := filepath.Join(f.dir(id), "rootfs.ext4")
	if base, err := os.Readlink(filepath.Join(f.dir(id), "base.ext4")); err == nil {
		writable, img.BaseDisk = filepath.Join(f.dir(id), "upper.ext4"), base
	}
	if err := copyFile(writable, img.Disk); err != nil {
		return img, err
	}
	return img, nil
}

// Resume replaces the sandbox's VM with one loaded from an image. The disk is
// copied into the VM's directory (the VM writes to it); memory and state are
// loaded in place, since Firecracker maps the memory file copy-on-write and
// never modifies it, so any number of forks can share one cached image.
func (f *Firecracker) Resume(ctx context.Context, id, workDir string, img engine.Image) error {
	f.Shutdown(ctx, id)
	d := f.dir(id)
	os.RemoveAll(d)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	if img.Disk == "" {
		return errors.New("a firecracker image needs its disk")
	}
	// lay the directory out the way the snapshot recorded it
	if img.BaseDisk != "" {
		base, err := filepath.Abs(img.BaseDisk)
		if err != nil {
			return err
		}
		if err := os.Symlink(base, filepath.Join(d, "base.ext4")); err != nil {
			return err
		}
		if err := copyFile(img.Disk, filepath.Join(d, "upper.ext4")); err != nil {
			return err
		}
	} else if err := copyFile(img.Disk, filepath.Join(d, "rootfs.ext4")); err != nil {
		return err
	}
	mem, err := filepath.Abs(img.Memory)
	if err != nil {
		return err
	}
	state, err := filepath.Abs(img.State)
	if err != nil {
		return err
	}
	if err := f.spawn(id); err != nil {
		return err
	}
	load := map[string]any{
		"snapshot_path":     state,
		"mem_backend":       map[string]any{"backend_type": "File", "backend_path": mem},
		"track_dirty_pages": true,
		"resume_vm":         true,
	}
	if err := f.api(id, "PUT", "/snapshot/load", load); err != nil {
		if !strings.Contains(err.Error(), "track_dirty_pages") {
			f.Shutdown(ctx, id)
			return err
		}
		// older Firecracker names the flag enable_diff_snapshots
		delete(load, "track_dirty_pages")
		load["enable_diff_snapshots"] = true
		f.kill(id)
		if err := f.spawn(id); err != nil {
			return err
		}
		if err := f.api(id, "PUT", "/snapshot/load", load); err != nil {
			f.Shutdown(ctx, id)
			return err
		}
	}
	if err := f.Rebase(ctx, id, img.Base); err != nil {
		return err
	}
	return f.waitAgent(id)
}

// kill stops the sandbox's Firecracker process, leaving its directory.
func (f *Firecracker) kill(id string) {
	if f.Running(context.Background(), id) {
		p := f.pid(id)
		syscall.Kill(p, syscall.SIGKILL)
		for i := 0; i < 200 && alive(p); i++ {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// Shutdown kills the sandbox's VM and removes its directory. Its state lives
// in checkpoints, not here.
func (f *Firecracker) Shutdown(ctx context.Context, id string) error {
	f.kill(id)
	os.RemoveAll(f.dir(id))
	return nil
}
