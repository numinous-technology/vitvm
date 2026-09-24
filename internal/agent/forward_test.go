package agent

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func freePort(t *testing.T) uint32 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return uint32(ln.Addr().(*net.TCPAddr).Port)
}

func TestForwardTunnelsGuestPortsToTheHost(t *testing.T) {
	ConfigRoot = t.TempDir()
	// the "host" end: whatever DialHost reaches answers with a banner and
	// echoes one line
	DialHost = func(port uint32) (io.ReadWriteCloser, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			b.Write([]byte("host:\n"))
			line, _ := bufio.NewReader(b).ReadString('\n')
			b.Write([]byte("echo " + line))
		}()
		return a, nil
	}
	defer func() { DialHost = nil }()
	port := freePort(t)
	conf := `{"remotes":{"gpu1":"tok@127.0.0.1:1#ff"}}`
	r, _, err := Call(serve(t, t.TempDir()), Request{Op: "forward", Ports: []uint32{port}, Config: conf}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(r.Path); string(b) != conf || r.Path != filepath.Join(ConfigRoot, GmuxConfig) {
		t.Fatalf("config at %s: %q", r.Path, b)
	}
	// asking again (as after a resume) is harmless
	if _, _, err := Call(serve(t, t.TempDir()), Request{Op: "forward", Ports: []uint32{port}}, nil); err != nil {
		t.Fatalf("forward must be idempotent: %v", err)
	}
	c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	banner, _ := br.ReadString('\n')
	c.Write([]byte("ping\n"))
	echo, _ := br.ReadString('\n')
	if banner != "host:\n" || echo != "echo ping\n" {
		t.Fatalf("tunnel carried %q then %q", banner, echo)
	}
}

func itoa(p uint32) string { return strconv.FormatUint(uint64(p), 10) }
