package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Port is the vsock port the guest agent listens on.
const Port = 1024

// Conn is one connection to the agent: writes go to W, replies are read from R
// (which may hold bytes buffered during the handshake).
type Conn struct {
	W io.WriteCloser
	R *bufio.Reader
}

// DialFirecracker connects to the guest agent through a Firecracker vsock
// unix socket: connect, send "CONNECT <port>", expect "OK <n>".
func DialFirecracker(udsPath string, timeout time.Duration) (*Conn, error) {
	c, err := net.DialTimeout("unix", udsPath, timeout)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(timeout))
	if _, err := fmt.Fprintf(c, "CONNECT %d\n", Port); err != nil {
		c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "OK ") {
		c.Close()
		return nil, fmt.Errorf("guest agent did not accept the connection (%q, %v)", strings.TrimSpace(line), err)
	}
	c.SetDeadline(time.Time{})
	return &Conn{W: c, R: br}, nil
}

// Call sends req (and body, for a write) and returns the reply and, for a
// read, the file's bytes.
func Call(c *Conn, req Request, body []byte) (Reply, []byte, error) {
	defer c.W.Close()
	b, _ := json.Marshal(req)
	if _, err := c.W.Write(append(b, '\n')); err != nil {
		return Reply{}, nil, err
	}
	if len(body) > 0 {
		if _, err := c.W.Write(body); err != nil {
			return Reply{}, nil, err
		}
	}
	line, err := c.R.ReadBytes('\n')
	if err != nil {
		return Reply{}, nil, fmt.Errorf("reading agent reply: %w", err)
	}
	var r Reply
	if err := json.Unmarshal(line, &r); err != nil {
		return Reply{}, nil, fmt.Errorf("bad agent reply %q: %w", line, err)
	}
	if r.Error != "" {
		return r, nil, fmt.Errorf("guest agent: %s", r.Error)
	}
	var data []byte
	if req.Op == "read" {
		data = make([]byte, r.Size)
		if _, err := io.ReadFull(c.R, data); err != nil {
			return r, nil, err
		}
	}
	return r, data, nil
}
