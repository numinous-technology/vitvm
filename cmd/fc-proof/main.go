// fc-proof boots a real Firecracker microVM with vitvm's driver, snapshots its
// memory, forks the snapshot twice, and reads each guest's console to show the
// forks resumed the live RAM: every fork keeps the original's in-RAM nonce
// (proving the memory was restored, not rebooted) and the counters continue and
// diverge. Run on a Linux host with /dev/kvm.
package main

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/numinous-technology/vitvm/internal/fcvm"
)

var stateRE = regexp.MustCompile(`STATE nonce=([0-9a-f]+) counter=(\d+)`)

// lastState reads a VM's console log and returns the newest nonce and counter.
func lastState(f *fcvm.Firecracker, id string) (string, int, bool) {
	data, err := os.ReadFile(f.LogPath(id))
	if err != nil {
		return "", 0, false
	}
	ms := stateRE.FindAllStringSubmatch(string(data), -1)
	if len(ms) == 0 {
		return "", 0, false
	}
	last := ms[len(ms)-1]
	n, _ := strconv.Atoi(last[2])
	return last[1], n, true
}

func waitState(f *fcvm.Firecracker, id string) (string, int) {
	for i := 0; i < 100; i++ {
		if nonce, c, ok := lastState(f, id); ok {
			return nonce, c
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", 0
}

func main() {
	ctx := context.Background()
	f, err := fcvm.New(fcvm.Config{
		FirecrackerBin: "/usr/local/bin/firecracker",
		KernelImage:    "/tmp/vmlinux",
		RootFS:         "/tmp/rootfs.ext4",
		RunDir:         "/tmp/vit-fc",
		BootArgs:       "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/vit-init",
		VCPUs:          1, MemMiB: 256, DisableVsock: true,
	})
	must(err)

	fmt.Println("booting microVM...")
	must(f.Boot(ctx, "vm", "/tmp/work"))
	defer f.Shutdown(ctx, "vm")
	nonce, c0 := waitState(f, "vm")
	fmt.Printf("original VM: nonce=%s counter=%d\n", nonce, c0)
	if nonce == "" {
		fmt.Println("FAIL: guest never reported state")
		os.Exit(1)
	}

	time.Sleep(1500 * time.Millisecond)
	_, cBefore, _ := lastState(f, "vm")
	fmt.Println("snapshotting memory...")
	dir := "/tmp/snap"
	os.MkdirAll(dir, 0o755)
	mem, state, err := f.Snapshot(ctx, "vm", dir)
	must(err)
	fi, _ := os.Stat(mem)
	fmt.Printf("snapshot taken at counter=%d, mem image %d bytes\n", cBefore, fi.Size())

	// stop the original so it cannot be confused with the forks, then fork twice
	f.Shutdown(ctx, "vm")
	must(f.Fork(ctx, "forkA", "/tmp/work", mem, state))
	defer f.Shutdown(ctx, "forkA")
	must(f.Fork(ctx, "forkB", "/tmp/work", mem, state))
	defer f.Shutdown(ctx, "forkB")

	na, a0 := waitState(f, "forkA")
	nb, b0 := waitState(f, "forkB")
	fmt.Printf("forkA resumed: nonce=%s counter=%d\n", na, a0)
	fmt.Printf("forkB resumed: nonce=%s counter=%d\n", nb, b0)

	// let them run independently
	time.Sleep(2 * time.Second)
	_, aRun, _ := lastState(f, "forkA")
	_, bRun, _ := lastState(f, "forkB")
	fmt.Printf("forkA after running: counter=%d (advanced %d)\n", aRun, aRun-a0)
	fmt.Printf("forkB after running: counter=%d (advanced %d)\n", bRun, bRun-b0)

	fmt.Println("---")
	memoryResumed := na == nonce && nb == nonce
	resumedNotRebooted := a0 >= cBefore-2 && b0 >= cBefore-2 // counter continued near the snapshot point
	advanced := aRun > a0 && bRun > b0
	if memoryResumed && resumedNotRebooted && advanced {
		fmt.Println("PASS: both forks resumed the snapshot's live RAM")
		fmt.Printf("  same in-RAM nonce as the original (%s), counter continued from ~%d, both kept running\n", nonce, cBefore)
	} else {
		fmt.Printf("FAIL: memoryResumed=%v resumedNotRebooted=%v advanced=%v\n", memoryResumed, resumedNotRebooted, advanced)
		os.Exit(1)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
