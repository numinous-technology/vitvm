// fakefc stands in for the firecracker binary in tests. It runs in the VM's
// directory like the real one and serves the same REST calls the driver makes.
// The guest's work tree is kept packed inside the drive file ("the disk"), and
// a boot nonce lives in ./ram ("the memory"): snapshot/create writes the RAM to
// the memory file and flushes the work tree into the disk; snapshot/load does
// the reverse. On PUT /vsock it listens on the given (relative) path with
// Firecracker's CONNECT handshake and serves the real guest agent. Every call
// is logged to ./calls.log with the working directory it ran in.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/numinous-technology/vitvm/internal/agent"
)

var drive string // path_on_host as given (relative to our cwd)

func main() {
	var sock string
	for i, a := range os.Args {
		if a == "--api-sock" && i+1 < len(os.Args) {
			sock = os.Args[i+1]
		}
	}
	cwd, _ := os.Getwd()
	logf, _ := os.OpenFile("calls.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(3)
	}
	http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(logf, "cwd=%s %s %s %s\n", cwd, r.Method, r.URL.Path, strings.TrimSpace(string(body)))
		var m map[string]any
		json.Unmarshal(body, &m)
		str := func(k string) string { s, _ := m[k].(string); return s }
		switch r.URL.Path {
		case "/drives/rootfs":
			drive = str("path_on_host")
		case "/vsock":
			go serveVsock(str("uds_path"))
		case "/actions":
			unpack(drive, "work")
			var b [8]byte
			rand.Read(b[:])
			os.WriteFile("ram", []byte(hex.EncodeToString(b[:])), 0o644)
		case "/snapshot/create":
			ram, _ := os.ReadFile("ram")
			os.WriteFile(str("mem_file_path"), ram, 0o644)
			st, _ := json.Marshal(map[string]string{"drive": drive, "vsock": vsockPath})
			os.WriteFile(str("snapshot_path"), st, 0o644)
			pack("work", drive) // the disk now holds the work tree, as of the pause
		case "/snapshot/load":
			var st map[string]string
			b, _ := os.ReadFile(str("snapshot_path"))
			json.Unmarshal(b, &st)
			drive = st["drive"] // relative: resolves inside *this* VM's directory
			mb, _ := m["mem_backend"].(map[string]any)
			path, _ := mb["backend_path"].(string)
			ram, _ := os.ReadFile(path)
			os.WriteFile("ram", ram, 0o644)
			unpack(drive, "work")
			go serveVsock(st["vsock"])
		}
		w.WriteHeader(204)
	}))
}

var vsockPath string

func serveVsock(path string) {
	vsockPath = path
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			br := bufio.NewReader(c)
			line, _ := br.ReadString('\n')
			if !strings.HasPrefix(line, "CONNECT ") {
				return
			}
			fmt.Fprintf(c, "OK 1073741824\n")
			abs, _ := filepath.Abs("work")
			agent.Handle(rw{br, c}, abs)
		}(c)
	}
}

type rw struct {
	io.Reader
	io.Writer
}

func pack(dir, file string) {
	files := map[string][]byte{}
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files[filepath.ToSlash(rel)], _ = os.ReadFile(p)
		}
		return nil
	})
	b, _ := json.Marshal(files)
	os.WriteFile(file, b, 0o644)
}

func unpack(file, dir string) {
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	b, _ := os.ReadFile(file)
	var files map[string][]byte
	if json.Unmarshal(b, &files) != nil {
		return
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, data, 0o644)
	}
}
