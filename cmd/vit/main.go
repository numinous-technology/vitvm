// Command vit is git for virtual machines: sandboxes that checkpoint at every
// step, so you can log, diff, read, rewind and fork any past step.
//
// Two backends. process runs each step as a local process and checkpoints the
// sandbox's files; it runs anywhere. firecracker runs the sandbox as a
// Firecracker microVM and checkpoints its files, memory and disk, so a fork
// resumes a live machine. Every command works the same on both.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/numinous-technology/vitvm/internal/engine"
)

const version = "0.2.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	run := map[string]func([]string) error{
		"init": cmdInit, "config": cmdConfig, "new": cmdNew, "run": cmdRun, "log": cmdLog,
		"ls": func([]string) error { return cmdLs() }, "status": func([]string) error { return cmdStatus() },
		"checkout": cmdCheckout, "diff": cmdDiff, "show": cmdShow, "fork": cmdFork,
		"stop": cmdStop, "use": cmdUse, "push": cmdPush, "pull": cmdPull,
	}
	switch {
	case cmd == "version":
		fmt.Println("vit", version)
		return
	case cmd == "-h" || cmd == "--help" || cmd == "help":
		usage()
		return
	case run[cmd] == nil:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err := run[cmd](args); err != nil {
		fmt.Fprintln(os.Stderr, "vit: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`vit: git for virtual machines. Sandboxes that checkpoint at every step.

  vit init                         create a repo (.vit) in the current directory
  vit config [KEY [VALUE]]         show or set configuration
  vit new [NAME] [--backend B]     start a sandbox (process or firecracker)
  vit run -- CMD...                run a command; the result is a checkpoint
  vit log [SANDBOX]                the checkpoints of a sandbox, newest first
  vit ls                           list sandboxes
  vit status                       the current sandbox and its head
  vit use SANDBOX                  make a sandbox current
  vit checkout CHECKPOINT          rewind the current sandbox to a checkpoint
  vit diff CK [CK2]                what changed at a checkpoint, or between two
  vit show CHECKPOINT PATH         print a file as it was at a checkpoint
  vit fork CHECKPOINT [NAME]       branch a new sandbox from any checkpoint
  vit stop [SANDBOX]               shut a sandbox's machine down (history kept)
  vit push [SANDBOX] --to URL      upload a sandbox's checkpoints to a remote
  vit pull CHECKPOINT --from URL   pull a checkpoint and fork it here

On the firecracker backend a checkpoint holds the machine's memory and disk as
well as its files: fork and checkout resume the live machine. Remotes are
s3://bucket/prefix, dir:///path or a path. See docs/.
`)
}

// repo discovery -----------------------------------------------------------

func repoDir() (string, error) {
	if v := os.Getenv("VIT_DIR"); v != "" {
		return v, nil
	}
	dir, _ := os.Getwd()
	for {
		p := filepath.Join(dir, ".vit")
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not a vit repo (run `vit init` first)")
		}
		dir = parent
	}
}

func openRepo() (*engine.Repo, string, *config, error) {
	root, err := repoDir()
	if err != nil {
		return nil, "", nil, err
	}
	repo, err := engine.OpenRepo(root)
	if err != nil {
		return nil, "", nil, err
	}
	cfg, err := loadConfig(root)
	return repo, root, cfg, err
}

// engineFor builds an engine over the repo with the named backend.
func engineFor(repo *engine.Repo, cfg *config, backend string) (*engine.Engine, error) {
	b, err := cfg.backend(backend)
	if err != nil {
		return nil, err
	}
	return engine.New(repo, b), nil
}

// sandboxEngine loads a sandbox (the current one when id is "") and an engine
// with the backend it was created with.
func sandboxEngine(id string) (*engine.Engine, *engine.Sandbox, string, error) {
	repo, root, cfg, err := openRepo()
	if err != nil {
		return nil, nil, "", err
	}
	if id == "" {
		id = currentSandbox(root)
	}
	if id == "" {
		return nil, nil, root, fmt.Errorf("no current sandbox (run `vit new`)")
	}
	s, err := repo.Sandbox(id)
	if err != nil {
		return nil, nil, root, err
	}
	e, err := engineFor(repo, cfg, s.Backend)
	return e, s, root, err
}

func currentSandbox(root string) string {
	b, _ := os.ReadFile(filepath.Join(root, "CURRENT"))
	return strings.TrimSpace(string(b))
}

func setCurrent(root, id string) error {
	return os.WriteFile(filepath.Join(root, "CURRENT"), []byte(id+"\n"), 0o644)
}

// commands -----------------------------------------------------------------

func cmdInit(args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}
	root := filepath.Join(dir, ".vit")
	if _, err := engine.OpenRepo(root); err != nil {
		return err
	}
	a, _ := filepath.Abs(root)
	fmt.Printf("initialised a vit repo at %s\n", a)
	return nil
}

func cmdNew(args []string) error {
	fs := parseFlags(args)
	repo, root, cfg, err := openRepo()
	if err != nil {
		return err
	}
	backend := fs.str("backend")
	if backend == "" {
		backend = cfg.defaultBackend()
	}
	e, err := engineFor(repo, cfg, backend)
	if err != nil {
		return err
	}
	name := ""
	if len(fs.rest) > 0 {
		name = fs.rest[0]
	}
	s, err := e.Create(name)
	if err != nil {
		return err
	}
	if err := setCurrent(root, s.ID); err != nil {
		return err
	}
	fmt.Printf("created sandbox %s (%s) on %s, now current\n", s.Name, s.ID, s.Backend)
	return nil
}

func cmdRun(args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("a command is required: vit run -- CMD...")
	}
	e, s, _, err := sandboxEngine("")
	if err != nil {
		return err
	}
	c, err := e.Run(context.Background(), s, args, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	fmt.Printf("\n[%s step %d] exit %d, %s%s\n", c.ID, c.Seq, c.ExitCode, changeSummary(c), machineTag(c))
	return nil
}

func cmdLog(args []string) error {
	id := ""
	if len(args) > 0 {
		id = args[0]
	}
	e, s, _, err := sandboxEngine(id)
	if err != nil {
		return err
	}
	hist, err := e.Repo().History(s.ID)
	if err != nil {
		return err
	}
	fmt.Printf("sandbox %s (%s) on %s", s.Name, s.ID, s.Backend)
	if s.ForkedFrom != "" {
		fmt.Printf(", forked from %s", s.ForkedFrom)
	}
	fmt.Println()
	for _, c := range hist {
		marker := "  "
		if c.ID == s.Head {
			marker = "* "
		}
		fmt.Printf("%s%s  step %-3d %-8s %s%s  %s\n", marker, c.ID, c.Seq, exitLabel(c), changeSummary(c), machineTag(c), commandOrNote(c))
	}
	return nil
}

func cmdLs() error {
	repo, root, cfg, err := openRepo()
	if err != nil {
		return err
	}
	cur := currentSandbox(root)
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "\tID\tNAME\tBACKEND\tMACHINE\tSTEPS\tFORKED FROM\tCREATED")
	for _, s := range repo.Sandboxes() {
		mark := " "
		if s.ID == cur {
			mark = "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", mark, s.ID, s.Name, s.Backend, machineState(cfg, s),
			s.Steps, dash(s.ForkedFrom), s.Created.Format(time.RFC3339))
	}
	return tw.Flush()
}

func machineState(cfg *config, s *engine.Sandbox) string {
	b, err := cfg.backend(s.Backend)
	if err != nil {
		return "?"
	}
	mb, ok := b.(engine.MemoryBackend)
	if !ok {
		return "-"
	}
	if mb.Running(context.Background(), s.ID) {
		return "running"
	}
	return "stopped"
}

func cmdStatus() error {
	e, s, _, err := sandboxEngine("")
	if err != nil {
		fmt.Println(err)
		return nil
	}
	_, _, cfg, _ := openRepo()
	fmt.Printf("on sandbox %s (%s), backend %s, machine %s\n", s.Name, s.ID, s.Backend, machineState(cfg, s))
	fmt.Printf("head %s, %d step(s)\n", s.Head, s.Steps)
	if s.Backend == "process" || s.Backend == "" {
		fmt.Printf("files in %s\n", e.Repo().WorkDir(s.ID))
	}
	return nil
}

func cmdCheckout(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vit checkout CHECKPOINT")
	}
	e, s, _, err := sandboxEngine("")
	if err != nil {
		return err
	}
	if err := e.Checkout(s, args[0]); err != nil {
		return err
	}
	c, _ := e.Repo().Checkpoint(args[0])
	how := "files restored"
	if c != nil && c.HasMemory() {
		how = "machine resumed from that step"
	}
	fmt.Printf("checked out %s; %s\n", args[0], how)
	return nil
}

func cmdDiff(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vit diff CK [CK2]")
	}
	repo, _, cfg, err := openRepo()
	if err != nil {
		return err
	}
	e, _ := engineFor(repo, cfg, "process")
	from, to := "", args[0]
	if len(args) >= 2 {
		from, to = args[0], args[1]
	} else if c, err := repo.Checkpoint(args[0]); err == nil {
		from = c.Parent
	}
	changes, err := e.Diff(from, to)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Println("no changes")
		return nil
	}
	sym := map[string]string{"added": "+", "modified": "~", "deleted": "-"}
	for _, c := range changes {
		fmt.Printf("%s %s\n", sym[c.Kind], c.Path)
	}
	return nil
}

func cmdShow(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: vit show CHECKPOINT PATH")
	}
	repo, _, cfg, err := openRepo()
	if err != nil {
		return err
	}
	e, _ := engineFor(repo, cfg, "process")
	b, err := e.ReadFile(args[0], args[1])
	if err != nil {
		return err
	}
	os.Stdout.Write(b)
	return nil
}

func cmdFork(args []string) error {
	fs := parseFlags(args)
	if len(fs.rest) < 1 {
		return fmt.Errorf("usage: vit fork CHECKPOINT [NAME] [--backend B]")
	}
	repo, root, cfg, err := openRepo()
	if err != nil {
		return err
	}
	src, err := repo.Checkpoint(fs.rest[0])
	if err != nil {
		return err
	}
	backend := fs.str("backend")
	if backend == "" {
		if s, err := repo.Sandbox(src.Sandbox); err == nil {
			backend = s.Backend
		} else {
			backend = cfg.defaultBackend()
		}
	}
	e, err := engineFor(repo, cfg, backend)
	if err != nil {
		return err
	}
	name := ""
	if len(fs.rest) >= 2 {
		name = fs.rest[1]
	}
	s, err := e.Fork(src.ID, name)
	if err != nil {
		return err
	}
	if err := setCurrent(root, s.ID); err != nil {
		return err
	}
	how := "cold, from its files"
	if backend == "firecracker" && src.HasMemory() {
		how = "warm, the machine resumed from that step"
	}
	fmt.Printf("forked %s into sandbox %s (%s) on %s, %s; now current\n", src.ID, s.Name, s.ID, backend, how)
	return nil
}

func cmdStop(args []string) error {
	id := ""
	if len(args) > 0 {
		id = args[0]
	}
	e, s, _, err := sandboxEngine(id)
	if err != nil {
		return err
	}
	if err := e.Stop(context.Background(), s); err != nil {
		return err
	}
	fmt.Printf("stopped %s; the next run resumes from %s\n", s.Name, s.Head)
	return nil
}

func cmdUse(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vit use SANDBOX")
	}
	repo, root, _, err := openRepo()
	if err != nil {
		return err
	}
	s, err := repo.Sandbox(args[0])
	if err != nil {
		return err
	}
	if err := setCurrent(root, s.ID); err != nil {
		return err
	}
	fmt.Printf("now on sandbox %s (%s)\n", s.Name, s.ID)
	return nil
}

// helpers ------------------------------------------------------------------

func machineTag(c *engine.Checkpoint) string {
	if c.HasMemory() {
		return " +machine"
	}
	return ""
}

func changeSummary(c *engine.Checkpoint) string {
	if c.Added == 0 && c.Modified == 0 && c.Deleted == 0 {
		return "no file changes"
	}
	var parts []string
	if c.Added > 0 {
		parts = append(parts, fmt.Sprintf("+%d", c.Added))
	}
	if c.Modified > 0 {
		parts = append(parts, fmt.Sprintf("~%d", c.Modified))
	}
	if c.Deleted > 0 {
		parts = append(parts, fmt.Sprintf("-%d", c.Deleted))
	}
	return strings.Join(parts, " ")
}

func commandOrNote(c *engine.Checkpoint) string {
	if len(c.Command) > 0 {
		return strings.Join(c.Command, " ")
	}
	return "(" + c.Note + ")"
}

func exitLabel(c *engine.Checkpoint) string {
	if len(c.Command) == 0 {
		return ""
	}
	return fmt.Sprintf("exit %d", c.ExitCode)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
