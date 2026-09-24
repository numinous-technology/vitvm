// fakefc stands in for the firecracker binary in tests. It serves the REST API
// on the --api-sock unix socket, records every call to <sock>.calls, and
// creates the snapshot files a real firecracker would. No KVM, no VM.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
)

func main() {
	var sock string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--api-sock" && i+1 < len(args) {
			sock = args[i+1]
		}
	}
	if sock == "" {
		os.Exit(2)
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(3)
	}
	calls, _ := os.Create(sock + ".calls")
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(calls, "%s %s %s\n", r.Method, r.URL.Path, strings.TrimSpace(string(body)))
		calls.Sync()
		// on snapshot/create, write the files a real firecracker produces
		if r.URL.Path == "/snapshot/create" {
			var req struct {
				SnapshotPath string `json:"snapshot_path"`
				MemFilePath  string `json:"mem_file_path"`
			}
			json.Unmarshal(body, &req)
			os.WriteFile(req.MemFilePath, []byte("fake-memory-image"), 0o644)
			os.WriteFile(req.SnapshotPath, []byte("fake-vm-state"), 0o644)
		}
		w.WriteHeader(204)
	})
	http.Serve(ln, mux)
}
