package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

// loopbackUp brings up the loopback interface. The agent is the machine's
// init, so nothing else will; without it every connection to 127.0.0.1, the
// tunnels to gmux hosts included, fails with "network is unreachable". The
// kernel gives lo its 127.0.0.1 address once it is up.
func loopbackUp() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var ifr [40]byte // struct ifreq: 16-byte name, then the flags
	copy(ifr[:], "lo")
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCGIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); e != 0 {
		return fmt.Errorf("reading lo flags: %w", e)
	}
	flags := uint16(ifr[16]) | uint16(ifr[17])<<8
	flags |= syscall.IFF_UP | syscall.IFF_RUNNING
	ifr[16], ifr[17] = byte(flags), byte(flags>>8)
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.SIOCSIFFLAGS, uintptr(unsafe.Pointer(&ifr[0]))); e != 0 {
		return fmt.Errorf("bringing lo up: %w", e)
	}
	return nil
}
