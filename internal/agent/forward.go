package agent

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// A vitvm machine has no network device. It reaches the outside only through
// tunnels the host sets up: for each allowed destination the guest listens on
// 127.0.0.1:PORT, and every connection there is carried over vsock to the host
// on the same port, where vitvm forwards it to that one destination. A
// sandbox can therefore reach its gmux GPU hosts and nothing else.

// DialHost opens a stream to the host on a vsock port. The guest agent sets
// it to a real vsock dial; tests set it to whatever stands in for the host.
var DialHost func(port uint32) (io.ReadWriteCloser, error)

// ConfigRoot is prepended to the gmux config path, so tests do not write to
// the real /etc.
var ConfigRoot = "/"

// GmuxConfig is where the guest's gmux host config is installed.
const GmuxConfig = "etc/gmux/remotes.json"

var (
	listenMu  sync.Mutex
	listening = map[uint32]bool{}
)

// forward installs the gmux config and opens the requested ports. It is
// idempotent: a machine resumed from a snapshot already has its listeners, and
// asking again changes nothing.
func forward(req Request) (string, error) {
	path := filepath.Join(ConfigRoot, GmuxConfig)
	if req.Config != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(req.Config), 0o600); err != nil {
			return "", err
		}
	}
	for _, port := range req.Ports {
		if err := listen(port); err != nil {
			return "", err
		}
	}
	return path, nil
}

func listen(port uint32) error {
	listenMu.Lock()
	defer listenMu.Unlock()
	if listening[port] {
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listening on 127.0.0.1:%d: %w", port, err)
	}
	listening[port] = true
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				continue
			}
			go tunnel(c, port)
		}
	}()
	return nil
}

// tunnel carries one connection to the host and back.
func tunnel(c net.Conn, port uint32) {
	defer c.Close()
	if DialHost == nil {
		return
	}
	h, err := DialHost(port)
	if err != nil {
		return
	}
	defer h.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(h, c); done <- struct{}{} }()
	go func() { io.Copy(c, h); done <- struct{}{} }()
	<-done
}

// gmuxJobs lists gmux runs in progress in the guest, by scanning /proc. A
// checkpoint taken while one is running cannot carry its remote job into a
// fork, and vitvm says so.
func gmuxJobs() []string {
	var out []string
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		cmd, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := bytes.Split(bytes.TrimRight(cmd, "\x00"), []byte{0})
		if len(args) >= 2 && filepath.Base(string(args[0])) == "gmux" && string(args[1]) == "run" {
			out = append(out, strings.Join(strings.Fields(string(bytes.Join(args, []byte(" ")))), " "))
		}
	}
	return out
}
