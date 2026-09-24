package main

import (
	"fmt"
	"io"
	"syscall"
	"unsafe"
)

var ioEOF = io.EOF

// AF_VSOCK is not supported by Go's net package (it rejects the address family
// in getsockname), so the agent uses raw syscalls and wraps each connection as
// an *os.File, which is a plain ReadWriteCloser. This keeps vitvm free of
// external dependencies.
const (
	afVSOCK      = 40
	vmAddrCIDAny = 0xFFFFFFFF
	sockaddrVM   = 16
)

type vsockListener struct{ fd int }

// listenVsock binds and listens on AF_VSOCK port for any CID.
func listenVsock(port uint32) (*vsockListener, error) {
	fd, err := syscall.Socket(afVSOCK, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socket: %w", err)
	}
	var sa [sockaddrVM]byte
	sa[0] = byte(afVSOCK)
	sa[1] = byte(afVSOCK >> 8)
	sa[4] = byte(port)
	sa[5] = byte(port >> 8)
	sa[6] = byte(port >> 16)
	sa[7] = byte(port >> 24)
	sa[8], sa[9], sa[10], sa[11] = 0xFF, 0xFF, 0xFF, 0xFF // VMADDR_CID_ANY
	if _, _, e := syscall.Syscall(syscall.SYS_BIND, uintptr(fd), uintptr(unsafe.Pointer(&sa[0])), sockaddrVM); e != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("bind vsock: %w", e)
	}
	if err := syscall.Listen(fd, 16); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("listen: %w", err)
	}
	return &vsockListener{fd: fd}, nil
}

// Accept returns the next connection over a raw vsock fd. It calls accept
// directly rather than syscall.Accept, whose sockaddr parsing does not know
// AF_VSOCK and would close the fd it just accepted.
func (l *vsockListener) Accept() (*vsockConn, error) {
	var rsa [128]byte
	rl := uint32(len(rsa))
	nfd, _, e := syscall.Syscall(syscall.SYS_ACCEPT, uintptr(l.fd),
		uintptr(unsafe.Pointer(&rsa[0])), uintptr(unsafe.Pointer(&rl)))
	if e != 0 {
		return nil, e
	}
	return &vsockConn{fd: int(nfd)}, nil
}

// vsockConn is a connection over a raw AF_VSOCK fd. It uses blocking syscalls
// directly instead of *os.File, because Go's netpoller mishandles accepted
// vsock sockets (reads return EOF immediately).
type vsockConn struct{ fd int }

func (c *vsockConn) Read(p []byte) (int, error) {
	n, err := syscall.Read(c.fd, p)
	if n == 0 && err == nil {
		return 0, ioEOF
	}
	return n, err
}

func (c *vsockConn) Write(p []byte) (int, error) {
	total := 0
	for total < len(p) {
		n, err := syscall.Write(c.fd, p[total:])
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func (c *vsockConn) Close() error { return syscall.Close(c.fd) }
