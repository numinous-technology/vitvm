// vit-guest is the in-guest agent for the Firecracker backend. It runs as the
// microVM's init, listens on vsock, and runs commands the host sends. It also
// holds an in-RAM nonce and a counter, which the memory-checkpoint test uses to
// prove a restored snapshot resumes live memory rather than rebooting: every
// VM forked from one snapshot reports the same nonce (the snapshotted RAM),
// while a cold boot generates a new one.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

const port = 1024

var (
	nonce   string
	counter int64
)

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

func main() {
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	var b [8]byte
	rand.Read(b[:])
	nonce = hex.EncodeToString(b[:])
	go func() {
		for {
			time.Sleep(200 * time.Millisecond)
			atomic.AddInt64(&counter, 1)
		}
	}()
	// mount essentials so exec'd commands have a working environment
	exec.Command("mount", "-t", "proc", "proc", "/proc").Run()
	exec.Command("mkdir", "-p", "/work").Run()

	// Print the in-RAM nonce and counter to the console every half second. A
	// VM restored from a snapshot resumes this exact RAM, so its console keeps
	// the same nonce and a continuing counter; a cold boot shows a new nonce.
	go func() {
		for {
			fmt.Printf("STATE nonce=%s counter=%d\n", nonce, atomic.LoadInt64(&counter))
			time.Sleep(500 * time.Millisecond)
		}
	}()
	fmt.Println("vit-guest up, nonce", nonce)

	ln, err := listenVsock(port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vit-guest: vsock unavailable:", err)
		select {} // no vsock device configured; keep running for the console proof
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handle(conn)
	}
}

func handle(conn *vsockConn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	line = strings.TrimSpace(line)
	switch line {
	case "NONCE":
		fmt.Fprintln(conn, nonce)
	case "COUNTER":
		fmt.Fprintln(conn, atomic.LoadInt64(&counter))
	default:
		// treat the line as the first line of a JSON exec request
		var req execReq
		if json.Unmarshal([]byte(line), &req) != nil {
			return
		}
		resp := run(req)
		json.NewEncoder(conn).Encode(resp)
	}
}

func run(req execReq) execResp {
	if len(req.Cmd) == 0 {
		return execResp{Exit: -1, Stderr: "empty command"}
	}
	cmd := exec.Command(req.Cmd[0], req.Cmd[1:]...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}
	// the agent runs as init with no PATH, so give exec'd commands a sane one
	env := append([]string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}, os.Environ()...)
	cmd.Env = append(env, req.Env...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return execResp{Exit: -1, Stderr: err.Error()}
	}
	return execResp{Exit: code, Stdout: out.String(), Stderr: errb.String()}
}
