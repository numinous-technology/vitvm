// Package agent is the protocol between vitvm on the host and the agent inside
// a sandbox's machine. The guest agent (cmd/vit-guest) serves it over vsock;
// the Firecracker backend is the client.
//
// One request per connection. A request is one JSON line with an "op", and
// for "write" the file's bytes follow it. A reply is one JSON line, and for
// "read" the file's bytes follow it.
//
//	ping                              -> {"ok":true}
//	exec  {dir, env, cmd}             -> {"exit", "stdout", "stderr"}
//	tree                              -> {"entries":[{path,mode,size,hash,link,dir}]}
//	read  {path}                      -> {"size":n} + n bytes
//	write {path, mode, size|link|dir} + bytes -> {"ok":true}
//	clear                             -> {"ok":true}   (empties the work tree)
package agent

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one path in the work tree, in the same shape as tree.Entry.
type Entry struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	Size int64  `json:"size"`
	Hash string `json:"hash,omitempty"`
	Link string `json:"link,omitempty"`
	Dir  bool   `json:"dir,omitempty"`
}

// Request is the header line of a request.
type Request struct {
	Op    string   `json:"op"`
	Dir   string   `json:"dir,omitempty"`
	Env   []string `json:"env,omitempty"`
	Cmd   []string `json:"cmd,omitempty"`
	Path  string   `json:"path,omitempty"`
	Mode  uint32   `json:"mode,omitempty"`
	Size  int64    `json:"size,omitempty"`
	Link  string   `json:"link,omitempty"`
	IsDir bool     `json:"is_dir,omitempty"`
}

// Reply is the header line of a reply.
type Reply struct {
	OK      bool    `json:"ok,omitempty"`
	Error   string  `json:"error,omitempty"`
	Exit    int     `json:"exit"`
	Stdout  string  `json:"stdout,omitempty"`
	Stderr  string  `json:"stderr,omitempty"`
	Entries []Entry `json:"entries,omitempty"`
	Size    int64   `json:"size,omitempty"`
}

// Handle serves one request on rw against the work tree at root.
func Handle(rw io.ReadWriter, root string) {
	br := bufio.NewReader(rw)
	line, err := br.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	var req Request
	if err := json.Unmarshal(bytes.TrimSpace(line), &req); err != nil {
		reply(rw, Reply{Error: "bad request: " + err.Error()})
		return
	}
	if req.Op == "" && len(req.Cmd) > 0 {
		req.Op = "exec"
	}
	switch req.Op {
	case "ping":
		reply(rw, Reply{OK: true})
	case "exec":
		reply(rw, run(req, root))
	case "tree":
		entries, err := list(root)
		if err != nil {
			reply(rw, Reply{Error: err.Error()})
			return
		}
		reply(rw, Reply{OK: true, Entries: entries})
	case "read":
		p, err := inside(root, req.Path)
		if err != nil {
			reply(rw, Reply{Error: err.Error()})
			return
		}
		b, err := os.ReadFile(p)
		if err != nil {
			reply(rw, Reply{Error: err.Error()})
			return
		}
		reply(rw, Reply{OK: true, Size: int64(len(b))})
		rw.Write(b)
	case "write":
		if err := write(br, root, req); err != nil {
			reply(rw, Reply{Error: err.Error()})
			return
		}
		reply(rw, Reply{OK: true})
	case "clear":
		ents, _ := os.ReadDir(root)
		for _, e := range ents {
			os.RemoveAll(filepath.Join(root, e.Name()))
		}
		os.MkdirAll(root, 0o755)
		reply(rw, Reply{OK: true})
	default:
		reply(rw, Reply{Error: "unknown op " + req.Op})
	}
}

func reply(w io.Writer, r Reply) {
	b, _ := json.Marshal(r)
	w.Write(append(b, '\n'))
}

// inside resolves a work-tree relative path, refusing escapes.
func inside(root, rel string) (string, error) {
	rel = filepath.ToSlash(rel)
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains("/"+rel+"/", "/../") {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	return filepath.Join(root, filepath.FromSlash(rel)), nil
}

func run(req Request, root string) Reply {
	if len(req.Cmd) == 0 {
		return Reply{Exit: -1, Stderr: "empty command"}
	}
	cmd := exec.Command(req.Cmd[0], req.Cmd[1:]...)
	cmd.Dir = root
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	cmd.Env = append(os.Environ(), req.Env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return Reply{Exit: ee.ExitCode(), Stdout: out.String(), Stderr: errb.String()}
	case err != nil:
		return Reply{Exit: -1, Stderr: err.Error()}
	}
	return Reply{Exit: 0, Stdout: out.String(), Stderr: errb.String()}
}

// list walks the work tree. Files carry their sha256 so the host fetches only
// contents it lacks. Sockets, devices and pipes are skipped.
func list(root string) ([]Entry, error) {
	var out []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := uint32(info.Mode().Perm())
		switch {
		case d.IsDir():
			out = append(out, Entry{Path: rel, Mode: mode, Dir: true})
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out = append(out, Entry{Path: rel, Mode: mode, Link: target})
		case info.Mode().IsRegular():
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			n, err := io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			out = append(out, Entry{Path: rel, Mode: mode, Size: n, Hash: hex.EncodeToString(h.Sum(nil))})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

func write(r io.Reader, root string, req Request) error {
	p, err := inside(root, req.Path)
	if err != nil {
		return err
	}
	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0o644
	}
	switch {
	case req.IsDir:
		return os.MkdirAll(p, mode|0o700)
	case req.Link != "":
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.Remove(p)
		return os.Symlink(req.Link, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		io.CopyN(io.Discard, r, req.Size)
		return err
	}
	if _, err := io.CopyN(f, r, req.Size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
