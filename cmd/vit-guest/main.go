// vit-guest is the agent inside a vitvm Firecracker sandbox. It is the VM's
// init: as PID 1 it mounts the essentials, starts itself again as the agent,
// and reaps every orphaned process for the life of the VM (so background
// processes a sandbox starts do not pile up as zombies). The agent listens on
// vsock and serves the vitvm agent protocol (internal/agent) against /work.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/numinous-technology/vitvm/internal/agent"
)

const work = "/work"

func main() {
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	if os.Getpid() == 1 && os.Getenv("VIT_AGENT_CHILD") == "" {
		initMain()
		return
	}
	serve()
}

// initMain prepares the machine and supervises the agent, reaping all
// children, including orphans reparented to PID 1.
func initMain() {
	mounts := []struct{ src, dst, fs string }{
		{"proc", "/proc", "proc"},
		{"sysfs", "/sys", "sysfs"},
		{"devtmpfs", "/dev", "devtmpfs"},
		{"tmpfs", "/dev/shm", "tmpfs"},
		{"devpts", "/dev/pts", "devpts"},
		{"tmpfs", "/run", "tmpfs"},
	}
	for _, m := range mounts {
		os.MkdirAll(m.dst, 0o755)
		syscall.Mount(m.src, m.dst, m.fs, 0, "")
	}
	os.MkdirAll(work, 0o755)
	syscall.Sethostname([]byte("vit"))
	for {
		child := exec.Command("/proc/self/exe")
		child.Env = append(os.Environ(), "VIT_AGENT_CHILD=1")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "vit-guest: starting agent:", err)
			time.Sleep(time.Second)
			continue
		}
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, 0, nil)
			if err == syscall.EINTR {
				continue
			}
			if pid == child.Process.Pid || err == syscall.ECHILD {
				break // the agent died: start it again
			}
		}
	}
}

func serve() {
	ln, err := listenVsock(agent.Port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vit-guest: vsock:", err)
		os.Exit(1)
	}
	fmt.Println("vit-guest: agent listening on vsock port", agent.Port)
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer conn.Close()
			agent.Handle(conn, work)
		}()
	}
}
