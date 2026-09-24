package agent

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serve runs the agent on one end of a pipe and returns a Conn to the other.
func serve(t *testing.T, root string) *Conn {
	t.Helper()
	a, b := net.Pipe()
	go func() { Handle(a, root); a.Close() }()
	return &Conn{W: b, R: bufio.NewReader(b)}
}

func TestAgentOps(t *testing.T) {
	root := t.TempDir()
	if r, _, err := Call(serve(t, root), Request{Op: "ping"}, nil); err != nil || !r.OK {
		t.Fatalf("ping: %+v %v", r, err)
	}
	body := []byte("print('hi')\n")
	if _, _, err := Call(serve(t, root), Request{Op: "write", Path: "src/main.py", Mode: 0o755, Size: int64(len(body))}, body); err != nil {
		t.Fatal(err)
	}
	Call(serve(t, root), Request{Op: "write", Path: "link", Link: "src/main.py"}, nil)
	Call(serve(t, root), Request{Op: "write", Path: "empty-dir", IsDir: true}, nil)
	r, _, err := Call(serve(t, root), Request{Op: "exec", Cmd: []string{"sh", "-c", "cat src/main.py; echo err >&2; exit 3"}}, nil)
	if err != nil || r.Exit != 3 || r.Stdout != string(body) || strings.TrimSpace(r.Stderr) != "err" {
		t.Fatalf("exec: %+v %v", r, err)
	}
	r, _, _ = Call(serve(t, root), Request{Op: "tree"}, nil)
	got := map[string]Entry{}
	for _, e := range r.Entries {
		got[e.Path] = e
	}
	if got["src/main.py"].Hash == "" || got["src/main.py"].Mode&0o100 == 0 || got["link"].Link != "src/main.py" || !got["empty-dir"].Dir || !got["src"].Dir {
		t.Fatalf("tree: %+v", r.Entries)
	}
	_, data, err := Call(serve(t, root), Request{Op: "read", Path: "src/main.py"}, nil)
	if err != nil || string(data) != string(body) {
		t.Fatalf("read: %q %v", data, err)
	}
	if _, _, err := Call(serve(t, root), Request{Op: "read", Path: "../etc/passwd"}, nil); err == nil {
		t.Fatal("reads outside the work tree must be refused")
	}
	if _, _, err := Call(serve(t, root), Request{Op: "clear"}, nil); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(root); len(ents) != 0 {
		t.Fatalf("clear left %d entries", len(ents))
	}
	if _, err := os.Stat(filepath.Join(root)); err != nil {
		t.Fatal("clear must keep the work root")
	}
}
