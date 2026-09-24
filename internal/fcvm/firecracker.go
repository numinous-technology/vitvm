// Package fcvm is vitvm's Firecracker backend: it runs a sandbox as a
// Firecracker microVM and snapshots the guest's memory and device state at
// each checkpoint, so a restore or a fork resumes a live process rather than
// replaying files. It implements engine.MemoryBackend.
//
// The driver speaks Firecracker's REST API over each VM's unix socket, using
// only the standard library. It needs a Linux host with /dev/kvm, the
// firecracker binary, a guest kernel and a root filesystem image.
package fcvm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Config points the driver at its host tools and guest images.
type Config struct {
	FirecrackerBin string // path to the firecracker binary
	KernelImage    string // uncompressed guest kernel (vmlinux)
	RootFS         string // guest root filesystem image (ext4)
	RunDir         string // where per-VM sockets and pidfiles live
	VCPUs          int    // guest vCPUs (default 1)
	MemMiB         int    // guest memory (default 512)
	BootArgs       string // kernel command line
	DisableVsock   bool   // skip the vsock device (no in-guest Exec)
}

// Firecracker is a MemoryBackend backed by real microVMs.
type Firecracker struct {
	cfg Config
	mu  sync.Mutex
	vms map[string]*vm
}

type vm struct {
	id     string
	sock   string
	proc   *exec.Cmd
	client *http.Client
}

// New builds the driver, checking the host is capable.
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
		cfg.BootArgs = "console=ttyS0 reboot=k panic=1 pci=off"
	}
	if cfg.RunDir == "" {
		cfg.RunDir = filepath.Join(os.TempDir(), "vit-fc")
	}
	if err := os.MkdirAll(cfg.RunDir, 0o755); err != nil {
		return nil, err
	}
	return &Firecracker{cfg: cfg, vms: map[string]*vm{}}, nil
}

// Name identifies the backend.
func (f *Firecracker) Name() string { return "firecracker" }

func unixClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
	}, Timeout: 30 * time.Second}
}

// api makes a Firecracker REST call over the VM's socket.
func (v *vm) api(method, path string, body any) error {
	var r *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, "http://localhost"+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		return fmt.Errorf("firecracker %s %s: %s: %s", method, path, resp.Status, string(buf[:n]))
	}
	return nil
}

// spawn launches a firecracker process bound to a fresh API socket.
func (f *Firecracker) spawn(ctx context.Context, id string) (*vm, error) {
	sock := filepath.Join(f.cfg.RunDir, id+".sock")
	os.Remove(sock)
	cmd := exec.Command(f.cfg.FirecrackerBin, "--api-sock", sock, "--id", id)
	logf, _ := os.Create(filepath.Join(f.cfg.RunDir, id+".log"))
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting firecracker: %w", err)
	}
	v := &vm{id: id, sock: sock, proc: cmd, client: unixClient(sock)}
	// wait for the API socket to accept a request
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := v.api("GET", "/", nil); err == nil || isAPIUp(err) {
			return v, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	cmd.Process.Kill()
	return nil, fmt.Errorf("firecracker API socket never came up for %s", id)
}

// isAPIUp reports whether an error means the API answered (any HTTP response),
// as opposed to the socket not being ready.
func isAPIUp(err error) bool {
	// once the socket accepts connections, GET / returns an HTTP error, not a
	// dial error; treat a non-nil HTTP status as "up".
	return err != nil && (bytes.Contains([]byte(err.Error()), []byte("firecracker")) ||
		bytes.Contains([]byte(err.Error()), []byte("Status")))
}

// Boot starts and configures the microVM.
func (f *Firecracker) Boot(ctx context.Context, id, workDir string) error {
	f.mu.Lock()
	if _, ok := f.vms[id]; ok {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()
	v, err := f.spawn(ctx, id)
	if err != nil {
		return err
	}
	if err := v.api("PUT", "/boot-source", map[string]any{
		"kernel_image_path": f.cfg.KernelImage, "boot_args": f.cfg.BootArgs,
	}); err != nil {
		return err
	}
	if err := v.api("PUT", "/drives/rootfs", map[string]any{
		"drive_id": "rootfs", "path_on_host": f.cfg.RootFS,
		"is_root_device": true, "is_read_only": false,
	}); err != nil {
		return err
	}
	if err := v.api("PUT", "/machine-config", map[string]any{
		"vcpu_count": f.cfg.VCPUs, "mem_size_mib": f.cfg.MemMiB,
	}); err != nil {
		return err
	}
	// a vsock device so the host can reach the in-guest agent for Exec
	if !f.cfg.DisableVsock {
		os.Remove(vsockUDS(f.cfg.RunDir, id)) // clear a stale host-side socket
		if err := v.api("PUT", "/vsock", map[string]any{
			"guest_cid": 3, "uds_path": vsockUDS(f.cfg.RunDir, id),
		}); err != nil {
			return err
		}
	}
	if err := v.api("PUT", "/actions", map[string]any{"action_type": "InstanceStart"}); err != nil {
		return err
	}
	f.mu.Lock()
	f.vms[id] = v
	f.mu.Unlock()
	return nil
}

// Snapshot pauses the VM, writes a full snapshot (memory image + state), and
// resumes it.
func (f *Firecracker) Snapshot(ctx context.Context, id, dir string) (string, string, error) {
	f.mu.Lock()
	v := f.vms[id]
	f.mu.Unlock()
	if v == nil {
		return "", "", fmt.Errorf("sandbox %s is not running", id)
	}
	memPath := filepath.Join(dir, "mem")
	statePath := filepath.Join(dir, "state")
	if err := v.api("PATCH", "/vm", map[string]any{"state": "Paused"}); err != nil {
		return "", "", err
	}
	if err := v.api("PUT", "/snapshot/create", map[string]any{
		"snapshot_type": "Full", "snapshot_path": statePath, "mem_file_path": memPath,
	}); err != nil {
		return "", "", err
	}
	if err := v.api("PATCH", "/vm", map[string]any{"state": "Resumed"}); err != nil {
		return "", "", err
	}
	return memPath, statePath, nil
}

// loadFrom launches a fresh VM and loads a snapshot into it, resuming.
func (f *Firecracker) loadFrom(ctx context.Context, id, memPath, statePath string) error {
	v, err := f.spawn(ctx, id)
	if err != nil {
		return err
	}
	if err := v.api("PUT", "/snapshot/load", map[string]any{
		"snapshot_path": statePath, "mem_file_path": memPath, "resume_vm": true,
		"enable_diff_snapshots": false,
	}); err != nil {
		return err
	}
	f.mu.Lock()
	f.vms[id] = v
	f.mu.Unlock()
	return nil
}

// Restore resumes the sandbox's machine from a snapshot.
func (f *Firecracker) Restore(ctx context.Context, id, workDir, memPath, statePath string) error {
	f.Shutdown(ctx, id)
	return f.loadFrom(ctx, id, memPath, statePath)
}

// Fork resumes a copy of a snapshot as a new sandbox.
func (f *Firecracker) Fork(ctx context.Context, newID, workDir, memPath, statePath string) error {
	return f.loadFrom(ctx, newID, memPath, statePath)
}

// Shutdown stops the sandbox's machine.
func (f *Firecracker) Shutdown(ctx context.Context, id string) error {
	f.mu.Lock()
	v := f.vms[id]
	delete(f.vms, id)
	f.mu.Unlock()
	if v == nil {
		return nil
	}
	if v.proc != nil && v.proc.Process != nil {
		v.proc.Process.Kill()
		v.proc.Wait()
	}
	os.Remove(v.sock)
	return nil
}

// LogPath returns the file that captures a VM's console and Firecracker log.
func (f *Firecracker) LogPath(id string) string {
	return filepath.Join(f.cfg.RunDir, id+".log")
}
