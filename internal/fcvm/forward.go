package fcvm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/numinous-technology/vitvm/internal/agent"
)

// setupForwards starts a forwarder per gmux host next to the VM and has the
// guest open the matching ports and install its gmux config. It runs after
// every boot and resume, so a fork gets forwarders of its own; asking a
// resumed guest again is harmless.
func (f *Firecracker) setupForwards(id string) error {
	if len(f.cfg.Forwards) == 0 {
		return nil
	}
	if f.cfg.ForwarderBin == "" {
		return fmt.Errorf("gmux hosts are configured but the forwarder binary is not")
	}
	f.killForwarders(id)
	type remotes struct {
		Default string            `json:"default"`
		Remotes map[string]string `json:"remotes"`
	}
	conf := remotes{Default: f.cfg.Forwards[0].Name, Remotes: map[string]string{}}
	var ports []uint32
	var pids []string
	for i, fw := range f.cfg.Forwards {
		port := uint32(f.cfg.GuestPortBase + i)
		uds := filepath.Join(f.dir(id), fmt.Sprintf("v.sock_%d", port))
		os.Remove(uds)
		logf, err := os.OpenFile(filepath.Join(f.dir(id), "forward.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		cmd := exec.Command(f.cfg.ForwarderBin, "__forward", uds, fw.Target)
		cmd.Stdout, cmd.Stderr = logf, logf
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		err = cmd.Start()
		logf.Close()
		if err != nil {
			return fmt.Errorf("starting the forwarder for %s: %w", fw.Name, err)
		}
		pids = append(pids, strconv.Itoa(cmd.Process.Pid))
		cmd.Process.Release()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(uds); err == nil {
				break
			}
		}
		ports = append(ports, port)
		conf.Remotes[fw.Name] = fmt.Sprintf("%s@127.0.0.1:%d#%s", fw.Token, port, fw.Fingerprint)
	}
	os.WriteFile(filepath.Join(f.dir(id), "forward.pids"), []byte(strings.Join(pids, "\n")), 0o644)
	cj, _ := json.Marshal(conf)
	r, _, err := f.call(id, agent.Request{Op: "forward", Ports: ports, Config: string(cj)}, nil)
	if err != nil {
		return fmt.Errorf("opening gmux ports in the guest: %w", err)
	}
	return os.WriteFile(filepath.Join(f.dir(id), "gmux-config"), []byte(r.Path), 0o644)
}

func (f *Firecracker) killForwarders(id string) {
	b, err := os.ReadFile(filepath.Join(f.dir(id), "forward.pids"))
	if err != nil {
		return
	}
	for _, line := range strings.Fields(string(b)) {
		if p, err := strconv.Atoi(line); err == nil && alive(p) {
			syscall.Kill(p, syscall.SIGKILL)
		}
	}
	os.Remove(filepath.Join(f.dir(id), "forward.pids"))
}

// gmuxEnv is what a command in the sandbox needs to use its gmux hosts:
// where the config is, a session prefix unique to this sandbox (so a fork
// never shares the parent's remote workspace), and labels that account GPU
// time to the sandbox and the step.
func (f *Firecracker) gmuxEnv(id string, step string) []string {
	env := []string{"GMUX_SESSION_PREFIX=" + id, "GMUX_OWNER=vit-" + id}
	if step != "" {
		env = append(env, "GMUX_NAME=vit-"+id+"-step"+step)
	}
	if b, err := os.ReadFile(filepath.Join(f.dir(id), "gmux-config")); err == nil && len(b) > 0 {
		env = append(env, "GMUX_CONFIG="+string(b))
	}
	return env
}

// InFlight lists gmux runs still going in the guest.
func (f *Firecracker) InFlight(ctx context.Context, id string) ([]string, error) {
	r, _, err := f.call(id, agent.Request{Op: "jobs"}, nil)
	return r.Jobs, err
}
