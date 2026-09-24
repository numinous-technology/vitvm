package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/numinous-technology/vitvm/internal/engine"
	"github.com/numinous-technology/vitvm/internal/fcvm"
)

// Repo configuration lives in .vit/config.json. Every key can be overridden by
// an environment variable: firecracker.kernel -> VIT_FIRECRACKER_KERNEL.
var configKeys = map[string]string{
	"backend":               "backend for new sandboxes: process (default) or firecracker",
	"firecracker.bin":       "path to the firecracker binary (default: firecracker on PATH)",
	"firecracker.kernel":    "guest kernel (uncompressed vmlinux)",
	"firecracker.rootfs":    "base root filesystem (scripts/build-rootfs.sh)",
	"firecracker.vcpus":     "guest vCPUs (default 1)",
	"firecracker.mem_mib":   "guest memory in MiB (default 512)",
	"firecracker.run_dir":   "where running VMs keep their sockets and disks (default /tmp/vit-fc)",
	"firecracker.bootargs":  "guest kernel command line (default starts the vit agent)",
	"firecracker.disk_mode": "overlay (default: shared read-only base + small writable disk) or copy",
	"firecracker.upper_gib": "size of the sparse writable disk in overlay mode (default 8)",
}

type config struct {
	path string
	kv   map[string]string
}

func loadConfig(root string) (*config, error) {
	c := &config{path: filepath.Join(root, "config.json"), kv: map[string]string{}}
	b, err := os.ReadFile(c.path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &c.kv); err != nil {
			return nil, fmt.Errorf("%s: %w", c.path, err)
		}
	}
	return c, nil
}

func envName(key string) string {
	return "VIT_" + strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(key))
}

func (c *config) get(key string) string {
	if v := os.Getenv(envName(key)); v != "" {
		return v
	}
	return c.kv[key]
}

func (c *config) set(key, value string) error {
	if _, ok := configKeys[key]; !ok {
		return fmt.Errorf("unknown key %q (see `vit config`)", key)
	}
	if value == "" {
		delete(c.kv, key)
	} else {
		c.kv[key] = value
	}
	b, _ := json.MarshalIndent(c.kv, "", "  ")
	return os.WriteFile(c.path, append(b, '\n'), 0o644)
}

func (c *config) intv(key string) int {
	n, _ := strconv.Atoi(c.get(key))
	return n
}

// backend builds the named backend from the configuration.
func (c *config) backend(name string) (engine.Backend, error) {
	switch name {
	case "", "process":
		return engine.ProcessBackend{}, nil
	case "firecracker":
		return fcvm.New(fcvm.Config{
			FirecrackerBin: c.get("firecracker.bin"),
			KernelImage:    abs(c.get("firecracker.kernel")),
			RootFS:         abs(c.get("firecracker.rootfs")),
			RunDir:         c.get("firecracker.run_dir"),
			VCPUs:          c.intv("firecracker.vcpus"),
			MemMiB:         c.intv("firecracker.mem_mib"),
			BootArgs:       c.get("firecracker.bootargs"),
			DiskMode:       c.get("firecracker.disk_mode"),
			UpperGiB:       c.intv("firecracker.upper_gib"),
		})
	}
	return nil, fmt.Errorf("unknown backend %q (process or firecracker)", name)
}

func abs(p string) string {
	if p == "" {
		return ""
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func cmdConfig(args []string) error {
	_, root, cfg, err := openRepo()
	if err != nil {
		return err
	}
	switch len(args) {
	case 0:
		keys := make([]string, 0, len(configKeys))
		for k := range configKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := cfg.get(k)
			if v == "" {
				v = "-"
			}
			fmt.Printf("%-22s %-40s %s\n", k, v, configKeys[k])
		}
		_ = root
		return nil
	case 1:
		fmt.Println(cfg.get(args[0]))
		return nil
	default:
		return cfg.set(args[0], args[1])
	}
}
