package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/numinous-technology/vitvm/internal/snap"
	"github.com/numinous-technology/vitvm/internal/tree"
)

// FakeMachine is a MemoryBackend and GuestFS with no hypervisor, for tests and
// CI. Each sandbox's "guest" is a directory the fake owns (commands run there
// as local processes), its "disk" is a serialisation of that directory, and
// its "memory" is a byte buffer tests can change. Snapshot, resume, warm and
// cold forks therefore move real files and real bytes through the engine, the
// same way the Firecracker backend moves a real disk and memory image.
type FakeMachine struct {
	root string
	mu   sync.Mutex
	ram  map[string][]byte // sandbox id -> memory; present means running
}

// NewFakeMachine keeps guest directories under root.
func NewFakeMachine(root string) *FakeMachine {
	return &FakeMachine{root: root, ram: map[string][]byte{}}
}

func (f *FakeMachine) Name() string           { return "fake-machine" }
func (f *FakeMachine) guest(id string) string { return filepath.Join(f.root, id, "work") }

// Boot starts a fresh machine: empty guest directory, fresh memory.
func (f *FakeMachine) Boot(ctx context.Context, id, workDir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, up := f.ram[id]; up {
		return nil
	}
	os.RemoveAll(f.guest(id))
	if err := os.MkdirAll(f.guest(id), 0o755); err != nil {
		return err
	}
	f.ram[id] = []byte("boot;")
	return nil
}

func (f *FakeMachine) Running(ctx context.Context, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, up := f.ram[id]
	return up
}

// Exec runs the command in the guest directory.
func (f *FakeMachine) Exec(ctx context.Context, id, workDir string, command []string, env []string, stdout, stderr io.Writer) (int, error) {
	if !f.Running(ctx, id) {
		return -1, fmt.Errorf("sandbox %s is not running", id)
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = f.guest(id), env, stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// ListFiles walks the guest directory, hashing files the way the guest agent
// does.
func (f *FakeMachine) ListFiles(ctx context.Context, id string) ([]tree.Entry, error) {
	t, err := tree.Snapshot(f.guest(id), hasher{}, nil)
	if err != nil {
		return nil, err
	}
	return t.Entries, nil
}

func (f *FakeMachine) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.guest(id), filepath.FromSlash(path)))
}

func (f *FakeMachine) WriteTree(ctx context.Context, id string, t *tree.Tree, blob func(string) ([]byte, error)) error {
	return snap.Restore(t, blobReader(blob), f.guest(id))
}

// Snapshot writes memory, a state marker, and the guest directory as a disk.
func (f *FakeMachine) Snapshot(ctx context.Context, id, dir string) (Image, error) {
	f.mu.Lock()
	ram := append([]byte(nil), f.ram[id]...)
	f.mu.Unlock()
	img := Image{Memory: filepath.Join(dir, "mem"), State: filepath.Join(dir, "state"), Disk: filepath.Join(dir, "disk")}
	if err := os.WriteFile(img.Memory, ram, 0o600); err != nil {
		return Image{}, err
	}
	if err := os.WriteFile(img.State, []byte("fake-vcpu-state"), 0o600); err != nil {
		return Image{}, err
	}
	disk, err := packDir(f.guest(id))
	if err != nil {
		return Image{}, err
	}
	return img, os.WriteFile(img.Disk, disk, 0o600)
}

// Resume replaces the sandbox's machine with the image's memory and disk.
func (f *FakeMachine) Resume(ctx context.Context, id, workDir string, img Image) error {
	ram, err := os.ReadFile(img.Memory)
	if err != nil {
		return err
	}
	disk, err := os.ReadFile(img.Disk)
	if err != nil {
		return err
	}
	if err := unpackDir(disk, f.guest(id)); err != nil {
		return err
	}
	f.mu.Lock()
	f.ram[id] = ram
	f.mu.Unlock()
	return nil
}

func (f *FakeMachine) Shutdown(ctx context.Context, id string) error {
	f.mu.Lock()
	delete(f.ram, id)
	f.mu.Unlock()
	return nil
}

// Memory and Touch let tests read and change a machine's memory.
func (f *FakeMachine) Memory(id string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.ram[id]...)
}

func (f *FakeMachine) Touch(id string, b []byte) {
	f.mu.Lock()
	f.ram[id] = append(f.ram[id], b...)
	f.mu.Unlock()
}

// GuestDir exposes a sandbox's guest directory to tests.
func (f *FakeMachine) GuestDir(id string) string { return f.guest(id) }

type hasher struct{}

func (hasher) PutFile(path string) (string, int64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), int64(len(b)), nil
}

type blobReader func(string) ([]byte, error)

func (r blobReader) OpenBlob(hash string) (io.ReadCloser, error) {
	b, err := r(hash)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytesReader(b)), nil
}

func packDir(dir string) ([]byte, error) {
	files := map[string][]byte{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, err := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = b
		return err
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(files)
}

func unpackDir(b []byte, dir string) error {
	var files map[string][]byte
	if err := json.Unmarshal(b, &files); err != nil {
		return err
	}
	os.RemoveAll(dir)
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
	}
	return os.MkdirAll(dir, 0o755)
}
