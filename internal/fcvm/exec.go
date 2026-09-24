package fcvm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"
)

// The guest runs a small agent listening on a vsock port. The host reaches it
// through the VM's vsock unix-domain socket: it connects, writes
// "CONNECT <port>\n", Firecracker replies "OK <n>\n", and the rest of the
// stream is the guest agent. The agent protocol is one JSON request and one
// JSON response:
//
//	-> {"dir":"/work","env":["K=V"],"cmd":["sh","-c","..."]}
//	<- {"exit":0,"stdout":"...","stderr":"..."}
const guestAgentPort = 1024

type execReq struct {
	Dir string   `json:"dir"`
	Env []string `json:"env"`
	Cmd []string `json:"cmd"`
}
type execResp struct {
	Exit   int    `json:"exit"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// Exec runs a command in the guest through the agent over vsock.
func (f *Firecracker) Exec(ctx context.Context, workDir string, command []string, env []string, stdout, stderr io.Writer) (int, error) {
	f.mu.Lock()
	var v *vm
	for _, cand := range f.vms {
		v = cand // the engine runs one command per booted sandbox at a time
		break
	}
	f.mu.Unlock()
	if v == nil {
		return -1, fmt.Errorf("no running microVM to exec in")
	}
	conn, err := net.DialTimeout("unix", vsockUDS(f.cfg.RunDir, v.id), 10*time.Second)
	if err != nil {
		return -1, fmt.Errorf("connecting to guest vsock: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", guestAgentPort); err != nil {
		return -1, err
	}
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // "OK <n>"
		return -1, fmt.Errorf("guest vsock did not accept the connection: %w", err)
	}
	if err := json.NewEncoder(conn).Encode(execReq{Dir: "/work", Env: env, Cmd: command}); err != nil {
		return -1, err
	}
	var resp execResp
	if err := json.NewDecoder(br).Decode(&resp); err != nil {
		return -1, fmt.Errorf("reading guest agent reply: %w", err)
	}
	io.WriteString(stdout, resp.Stdout)
	io.WriteString(stderr, resp.Stderr)
	return resp.Exit, nil
}

func vsockUDS(runDir, id string) string { return filepath.Join(runDir, id+".vsock") }

// QueryGuest sends a single line to the guest agent and returns its reply. The
// memory-checkpoint proof uses it to read the guest's in-RAM nonce and counter.
func (f *Firecracker) QueryGuest(ctx context.Context, id, line string) (string, error) {
	conn, err := net.DialTimeout("unix", vsockUDS(f.cfg.RunDir, id), 10*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", guestAgentPort); err != nil {
		return "", err
	}
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // "OK <n>"
		return "", err
	}
	if _, err := fmt.Fprintf(conn, "%s\n", line); err != nil {
		return "", err
	}
	reply, err := br.ReadString('\n')
	return strings.TrimSpace(reply), err
}
