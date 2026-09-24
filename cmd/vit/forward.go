package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/numinous-technology/vitvm/internal/fcvm"
)

// cmdForward is the forwarder the firecracker backend starts next to a VM, one
// per gmux host: it listens on the VM's vsock socket for a port and connects
// each stream to that one host. It is how a machine with no network reaches
// its GPU hosts, and nothing else.
//
//	vit __forward UDS HOST:PORT
func cmdForward(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: vit __forward UDS HOST:PORT")
	}
	uds, target := args[0], args[1]
	os.Remove(uds)
	ln, err := net.Listen("unix", uds)
	if err != nil {
		return err
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			t, err := net.DialTimeout("tcp", target, 10*time.Second)
			if err != nil {
				fmt.Fprintln(os.Stderr, "vit forward:", err)
				return
			}
			defer t.Close()
			done := make(chan struct{}, 2)
			go func() { io.Copy(t, c); done <- struct{}{} }()
			go func() { io.Copy(c, t); done <- struct{}{} }()
			<-done
		}(c)
	}
}

// parseGmuxRemotes reads NAME=TOKEN@HOST:PORT#FINGERPRINT,... as printed by
// `gmux serve --addr`.
func parseGmuxRemotes(v string) ([]fcvm.Forward, error) {
	var out []fcvm.Forward
	for _, item := range strings.Split(v, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, target, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("gmux.remotes: %q is not NAME=TOKEN@HOST:PORT#FINGERPRINT", item)
		}
		tok, rest, ok1 := strings.Cut(target, "@")
		hostport, fp, ok2 := strings.Cut(rest, "#")
		if !ok1 || !ok2 || tok == "" || fp == "" || hostport == "" {
			return nil, fmt.Errorf("gmux.remotes: %q is not NAME=TOKEN@HOST:PORT#FINGERPRINT", item)
		}
		out = append(out, fcvm.Forward{Name: name, Target: hostport, Token: tok, Fingerprint: fp})
	}
	return out, nil
}
