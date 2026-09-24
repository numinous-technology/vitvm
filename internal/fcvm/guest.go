package fcvm

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/numinous-technology/vitvm/internal/agent"
	"github.com/numinous-technology/vitvm/internal/tree"
)

// GuestWork is the working tree inside the guest.
const GuestWork = "/work"

func (f *Firecracker) call(id string, req agent.Request, body []byte) (agent.Reply, []byte, error) {
	c, err := agent.DialFirecracker(f.vsock(id), 10*time.Second)
	if err != nil {
		return agent.Reply{}, nil, fmt.Errorf("reaching the agent in %s: %w", id, err)
	}
	return agent.Call(c, req, body)
}

// waitAgent waits for the guest agent to answer after a boot or a resume.
func (f *Firecracker) waitAgent(id string) error {
	deadline := time.Now().Add(f.cfg.AgentTimeout)
	var last error
	for time.Now().Before(deadline) {
		if _, _, err := f.call(id, agent.Request{Op: "ping"}, nil); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("guest agent in %s did not answer within %s: %v (console: %s)", id, f.cfg.AgentTimeout, last, f.LogPath(id))
}

// Exec runs a command in the guest's work tree.
func (f *Firecracker) Exec(ctx context.Context, id, workDir string, command []string, env []string, stdout, stderr io.Writer) (int, error) {
	r, _, err := f.call(id, agent.Request{Op: "exec", Cmd: command, Env: guestEnv(env)}, nil)
	if err != nil {
		return -1, err
	}
	io.WriteString(stdout, r.Stdout)
	io.WriteString(stderr, r.Stderr)
	return r.Exit, nil
}

// guestEnv passes the caller's environment minus what belongs to the host.
func guestEnv(env []string) []string {
	hostOnly := []string{"PATH=", "HOME=", "PWD=", "OLDPWD=", "SHLVL=", "_=", "TMPDIR=", "SSH_", "XDG_", "DBUS_", "VIT_"}
	var out []string
	for _, kv := range env {
		keep := true
		for _, h := range hostOnly {
			if strings.HasPrefix(kv, h) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// ListFiles lists the guest work tree with content hashes.
func (f *Firecracker) ListFiles(ctx context.Context, id string) ([]tree.Entry, error) {
	r, _, err := f.call(id, agent.Request{Op: "tree"}, nil)
	if err != nil {
		return nil, err
	}
	out := make([]tree.Entry, 0, len(r.Entries))
	for _, e := range r.Entries {
		out = append(out, tree.Entry{Path: e.Path, Mode: e.Mode, Size: e.Size, Hash: e.Hash, Link: e.Link, Dir: e.Dir})
	}
	return out, nil
}

// ReadFile reads one file from the guest work tree.
func (f *Firecracker) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	_, data, err := f.call(id, agent.Request{Op: "read", Path: path}, nil)
	return data, err
}

// WriteTree replaces the guest work tree with t.
func (f *Firecracker) WriteTree(ctx context.Context, id string, t *tree.Tree, blob func(string) ([]byte, error)) error {
	if _, _, err := f.call(id, agent.Request{Op: "clear"}, nil); err != nil {
		return err
	}
	for _, e := range t.Entries { // sorted, so directories precede their contents
		req := agent.Request{Op: "write", Path: e.Path, Mode: e.Mode, Link: e.Link, IsDir: e.Dir}
		var body []byte
		if !e.Dir && e.Link == "" {
			b, err := blob(e.Hash)
			if err != nil {
				return err
			}
			body, req.Size = b, int64(len(b))
		}
		if _, _, err := f.call(id, req, body); err != nil {
			return fmt.Errorf("writing %s into the guest: %w", e.Path, err)
		}
	}
	return nil
}
