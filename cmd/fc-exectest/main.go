// fc-exectest boots one real microVM with vsock and runs commands in the guest
// through vitvm's driver, proving the in-guest Exec path (vit run) works on real
// Firecracker.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/numinous-technology/vitvm/internal/fcvm"
)

type buf struct{ b strings.Builder }

func (w *buf) Write(p []byte) (int, error) { w.b.Write(p); return len(p), nil }

func main() {
	ctx := context.Background()
	f, err := fcvm.New(fcvm.Config{
		FirecrackerBin: "/usr/local/bin/firecracker", KernelImage: "/tmp/vmlinux",
		RootFS: "/tmp/rootfs.ext4", RunDir: "/tmp/vit-fc",
		BootArgs: "console=ttyS0 reboot=k panic=1 pci=off init=/sbin/vit-init",
		VCPUs:    1, MemMiB: 256,
	})
	if err != nil {
		panic(err)
	}
	if err := f.Boot(ctx, "ev", "/tmp/work"); err != nil {
		fmt.Println("boot:", err)
		os.Exit(1)
	}
	defer f.Shutdown(ctx, "ev")
	time.Sleep(2 * time.Second)

	for _, cmd := range [][]string{
		{"uname", "-r"},
		{"sh", "-c", "echo hello-from-guest; echo written > /work/out.txt; cat /work/out.txt"},
	} {
		var out, errb buf
		code, err := f.Exec(ctx, "/work", cmd, nil, &out, &errb)
		fmt.Printf("cmd %v -> exit=%d err=%v\n  stdout: %s\n", cmd, code, err, strings.TrimSpace(out.b.String()))
		if errb.b.Len() > 0 {
			fmt.Printf("  stderr: %s\n", strings.TrimSpace(errb.b.String()))
		}
	}
}
