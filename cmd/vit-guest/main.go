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
	for _, m := range []struct{ src, dst, fs string }{
		{"proc", "/proc", "proc"}, {"sysfs", "/sys", "sysfs"}, {"devtmpfs", "/dev", "devtmpfs"},
	} {
		syscall.Mount(m.src, m.dst, m.fs, 0, "")
	}
	// with a second disk, the root is a read-only base: layer the writable
	// disk over it and move into the combined root
	if _, err := os.Stat("/dev/vdb"); err == nil {
		if err := overlayRoot(); err != nil {
			fmt.Fprintln(os.Stderr, "vit-guest: overlay root:", err)
		}
	}
	for _, m := range []struct{ src, dst, fs string }{
		{"tmpfs", "/dev/shm", "tmpfs"}, {"devpts", "/dev/pts", "devpts"}, {"tmpfs", "/run", "tmpfs"},
	} {
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

// overlayRoot mounts /dev/vdb, layers it over the read-only root with
// overlayfs, moves /proc, /sys and /dev across, and pivots into the result.
// All scratch mountpoints live on a tmpfs, since the base root is read-only.
func overlayRoot() error {
	if err := syscall.Mount("tmpfs", "/mnt", "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("tmpfs on /mnt: %w", err)
	}
	for _, d := range []string{"/mnt/upper", "/mnt/root"} {
		os.MkdirAll(d, 0o755)
	}
	if err := syscall.Mount("/dev/vdb", "/mnt/upper", "ext4", 0, "noinit_itable"); err != nil {
		return fmt.Errorf("mounting the writable disk: %w", err)
	}
	os.MkdirAll("/mnt/upper/upper", 0o755)
	os.MkdirAll("/mnt/upper/work", 0o755)
	opts := "lowerdir=/,upperdir=/mnt/upper/upper,workdir=/mnt/upper/work"
	if err := syscall.Mount("overlay", "/mnt/root", "overlay", 0, opts); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	for _, m := range []string{"/proc", "/sys", "/dev"} {
		if err := syscall.Mount(m, "/mnt/root"+m, "", syscall.MS_MOVE, ""); err != nil {
			return fmt.Errorf("moving %s: %w", m, err)
		}
	}
	os.MkdirAll("/mnt/root/.oldroot", 0o700)
	if err := syscall.PivotRoot("/mnt/root", "/mnt/root/.oldroot"); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := syscall.Chdir("/"); err != nil {
		return err
	}
	// the old root stays referenced by the overlay; hide it from the tree
	syscall.Unmount("/.oldroot", syscall.MNT_DETACH)
	os.Remove("/.oldroot")
	return nil
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
