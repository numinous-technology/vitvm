// Command vit is git for virtual machines: it runs sandboxes that checkpoint
// at every step, and lets you log, diff, read, rewind and fork any past step.
//
// This build ships the process backend, which checkpoints a sandbox's files
// after each command on any machine, with no hypervisor. The firecracker
// backend (docs/firecracker.md) adds live memory to a checkpoint.
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

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "new":
		err = cmdNew(args)
	case "run":
		err = cmdRun(args)
	case "log":
		err = cmdLog(args)
	case "ls":
		err = cmdLs()
	case "status":
		err = cmdStatus()
	case "checkout":
		err = cmdCheckout(args)
	case "diff":
		err = cmdDiff(args)
	case "show":
		err = cmdShow(args)
	case "fork":
		err = cmdFork(args)
	case "version":
		fmt.Println("vit", version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vit: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`vit: git for virtual machines. Sandboxes that checkpoint at every step.

  vit init                       create a repo in the current directory
  vit new [name]                 start a sandbox and make it current
  vit run -- CMD...              run a command; the result is a checkpoint
  vit log [sandbox]              the checkpoints of a sandbox, newest first
  vit ls                         list sandboxes
  vit status                     the current sandbox and where its head is
  vit checkout CHECKPOINT        rewind the current sandbox to a checkpoint
  vit diff CK [CK2]              what changed at a checkpoint, or between two
  vit show CHECKPOINT PATH       print a file as it was at a checkpoint
  vit fork CHECKPOINT [name]     branch a new sandbox from any checkpoint
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

func openEngine() (*engine.Engine, string, error) {
	root, err := repoDir()
	if err != nil {
		return nil, "", err
	}
	repo, err := engine.OpenRepo(root)
	if err != nil {
		return nil, "", err
	}
	return engine.New(repo, engine.ProcessBackend{}), root, nil
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
	abs, _ := filepath.Abs(root)
	fmt.Printf("initialised a vit repo at %s\n", abs)
	return nil
}

func cmdNew(args []string) error {
	e, root, err := openEngine()
	if err != nil {
		return err
	}
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	s, err := e.Create(name)
	if err != nil {
		return err
	}
	if err := setCurrent(root, s.ID); err != nil {
		return err
	}
	fmt.Printf("created sandbox %s (%s), now current\n", s.Name, s.ID)
	fmt.Printf("its files live in %s\n", e.Repo().WorkDir(s.ID))
	return nil
}

func cmdRun(args []string) error {
	if len(args) == 0 || args[0] != "--" {
		// allow: vit run -- cmd, or vit run cmd
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
	} else {
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("a command is required: vit run -- CMD...")
	}
	e, root, err := openEngine()
	if err != nil {
		return err
	}
	cur := currentSandbox(root)
	if cur == "" {
		return fmt.Errorf("no current sandbox (run `vit new`)")
	}
	s, err := e.Repo().Sandbox(cur)
	if err != nil {
		return err
	}
	c, err := e.Run(context.Background(), s, args, os.Stdout, os.Stderr)
	if err != nil {
		return err
	}
	fmt.Printf("\n[%s step %d] exit %d, %s\n", c.ID, c.Seq, c.ExitCode, changeSummary(c))
	return nil
}

func cmdLog(args []string) error {
	e, root, err := openEngine()
	if err != nil {
		return err
	}
	id := currentSandbox(root)
	if len(args) > 0 {
		id = args[0]
	}
	if id == "" {
		return fmt.Errorf("no sandbox given and none is current")
	}
	s, err := e.Repo().Sandbox(id)
	if err != nil {
		return err
	}
	hist, err := e.Repo().History(s.ID)
	if err != nil {
		return err
	}
	fmt.Printf("sandbox %s (%s)", s.Name, s.ID)
	if s.ForkedFrom != "" {
		fmt.Printf(", forked from %s", s.ForkedFrom)
	}
	fmt.Println()
	for _, c := range hist {
		marker := "  "
		if c.ID == s.Head {
			marker = "* "
		}
		fmt.Printf("%s%s  step %-3d %-8s %s  %s\n", marker, c.ID, c.Seq,
			exitLabel(c), changeSummary(c), commandOrNote(c))
	}
	return nil
}

func cmdLs() error {
	e, root, err := openEngine()
	if err != nil {
		return err
	}
	cur := currentSandbox(root)
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "\tID\tNAME\tBACKEND\tSTEPS\tFORKED FROM\tCREATED")
	for _, s := range e.Repo().Sandboxes() {
		mark := " "
		if s.ID == cur {
			mark = "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", mark, s.ID, s.Name, s.Backend, s.Steps,
			dash(s.ForkedFrom), s.Created.Format(time.RFC3339))
	}
	tw.Flush()
	return nil
}

func cmdStatus() error {
	e, root, err := openEngine()
	if err != nil {
		return err
	}
	cur := currentSandbox(root)
	if cur == "" {
		fmt.Println("no current sandbox (run `vit new`)")
		return nil
	}
	s, err := e.Repo().Sandbox(cur)
	if err != nil {
		return err
	}
	fmt.Printf("on sandbox %s (%s)\n", s.Name, s.ID)
	fmt.Printf("head %s, %d step(s)\n", s.Head, s.Steps)
	fmt.Printf("files in %s\n", e.Repo().WorkDir(s.ID))
	return nil
}

func cmdCheckout(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vit checkout CHECKPOINT")
	}
	e, root, err := openEngine()
	if err != nil {
		return err
	}
	s, err := e.Repo().Sandbox(currentSandbox(root))
	if err != nil {
		return err
	}
	if err := e.Checkout(s, args[0]); err != nil {
		return err
	}
	fmt.Printf("checked out %s; working files restored to that step\n", args[0])
	return nil
}

func cmdDiff(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vit diff CK [CK2]")
	}
	e, _, err := openEngine()
	if err != nil {
		return err
	}
	from, to := "", args[0]
	if len(args) >= 2 {
		from, to = args[0], args[1]
	} else {
		// diff a checkpoint against its parent
		if c, err := e.Repo().Checkpoint(args[0]); err == nil {
			from = c.Parent
		}
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
	e, _, err := openEngine()
	if err != nil {
		return err
	}
	b, err := e.ReadFile(args[0], args[1])
	if err != nil {
		return err
	}
	os.Stdout.Write(b)
	return nil
}

func cmdFork(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vit fork CHECKPOINT [name]")
	}
	e, root, err := openEngine()
	if err != nil {
		return err
	}
	name := ""
	if len(args) >= 2 {
		name = args[1]
	}
	s, err := e.Fork(args[0], name)
	if err != nil {
		return err
	}
	if err := setCurrent(root, s.ID); err != nil {
		return err
	}
	fmt.Printf("forked %s into sandbox %s (%s), now current\n", args[0], s.Name, s.ID)
	return nil
}

// helpers ------------------------------------------------------------------

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
