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

// drives by id, path_on_host as given (relative to our cwd). The guest's
// work tree lives on the writable "upper" drive when there is one.
var drives = map[string]string{}

func workDrive() string {
	if d := drives["upper"]; d != "" {
		return d
	}
	return drives["rootfs"]
}

// memory images are one 4 KiB page: the RAM contents, zero padded. lastRAM is
// the RAM at the last load or snapshot, for diff snapshots.
const page = 4096

var lastRAM []byte

func pageOf(b []byte) []byte { p := make([]byte, page); copy(p, b); return p }

func main() {
	var sock string
	for i, a := range os.Args {
		if a == "--api-sock" && i+1 < len(os.Args) {
			sock = os.Args[i+1]
		}
	}
	cwd, _ := os.Getwd()
	logf, _ := os.OpenFile("calls.log", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	// guest-to-host vsock, as Firecracker does it: port P reaches the unix
	// socket <uds_path>_P in the VM's directory
	agent.DialHost = func(port uint32) (io.ReadWriteCloser, error) {
		return net.Dial("unix", filepath.Join(cwd, fmt.Sprintf("%s_%d", vsockName, port)))
	}
	agent.ConfigRoot = filepath.Join(cwd, "sysroot")
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
		case "/drives/rootfs", "/drives/upper":
			drives[strings.TrimPrefix(r.URL.Path, "/drives/")] = str("path_on_host")
		case "/vsock":
			go serveVsock(str("uds_path"))
		case "/actions":
			unpack(workDrive(), "work")
			var b [8]byte
			rand.Read(b[:])
			os.WriteFile("ram", []byte(hex.EncodeToString(b[:])), 0o644)
		case "/snapshot/create":
			ram, _ := os.ReadFile("ram")
			if str("snapshot_type") == "Diff" {
				// sparse, full size; the page is written only if it changed
				f, _ := os.Create(str("mem_file_path"))
				f.Truncate(page)
				if string(ram) != string(lastRAM) {
					f.WriteAt(pageOf(ram), 0)
				}
				f.Close()
			} else {
				os.WriteFile(str("mem_file_path"), pageOf(ram), 0o644)
			}
			lastRAM = ram
			st, _ := json.Marshal(map[string]any{"drives": drives, "vsock": vsockPath})
			os.WriteFile(str("snapshot_path"), st, 0o644)
			pack("work", workDrive()) // the disk now holds the work tree, as of the pause
		case "/snapshot/load":
			var st struct {
				Drives map[string]string
				Vsock  string
			}
			b, _ := os.ReadFile(str("snapshot_path"))
			json.Unmarshal(b, &st)
			drives = st.Drives // relative: resolve inside *this* VM's directory
			mb, _ := m["mem_backend"].(map[string]any)
			path, _ := mb["backend_path"].(string)
			img, _ := os.ReadFile(path)
			ram := []byte(strings.TrimRight(string(img), "\x00"))
			os.WriteFile("ram", ram, 0o644)
			lastRAM = ram
			unpack(workDrive(), "work")
			go serveVsock(st.Vsock)
		}
		w.WriteHeader(204)
	}))
}

var vsockPath, vsockName string

func serveVsock(path string) {
	vsockPath, vsockName = path, path
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
	// a fresh disk (an empty ext4 image, possibly gigabytes sparse) holds no
	// packed tree; look at the first byte before reading the whole file
	f, err := os.Open(file)
	if err != nil {
		return
	}
	first := make([]byte, 1)
	f.Read(first)
	f.Close()
	if first[0] != '{' {
		return
	}
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
