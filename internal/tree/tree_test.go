package tree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/numinous-technology/vitvm/internal/cas"
)

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotAndDiff(t *testing.T) {
	store, _ := cas.Open(t.TempDir())
	dir := t.TempDir()
	write(t, dir, "a.txt", "one")
	write(t, dir, "sub/b.txt", "two")
	os.Symlink("a.txt", filepath.Join(dir, "link"))

	t1, err := Snapshot(dir, store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := t1.Find("a.txt"); !ok {
		t.Fatal("a.txt should be in the tree")
	}
	if e, ok := t1.Find("link"); !ok || e.Link != "a.txt" {
		t.Fatal("symlink recorded by target")
	}

	// change one file, add one, delete one
	write(t, dir, "a.txt", "one changed")
	write(t, dir, "c.txt", "three")
	os.Remove(filepath.Join(dir, "sub/b.txt"))

	t2, _ := Snapshot(dir, store, nil)
	got := map[string]string{}
	for _, c := range Diff(t1, t2) {
		got[c.Path] = c.Kind
	}
	if got["a.txt"] != "modified" || got["c.txt"] != "added" || got["sub/b.txt"] != "deleted" {
		t.Fatalf("unexpected diff: %v", got)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	tr := &Tree{Entries: []Entry{{Path: "x", Hash: "h", Size: 3}}}
	b, _ := tr.Marshal()
	back, err := Unmarshal(b)
	if err != nil || len(back.Entries) != 1 || back.Entries[0].Path != "x" {
		t.Fatalf("round trip failed: %v %v", back, err)
	}
}

func TestDiffFromNilIsAllAdded(t *testing.T) {
	tr := &Tree{Entries: []Entry{{Path: "a"}, {Path: "b"}}}
	ch := Diff(nil, tr)
	if len(ch) != 2 || ch[0].Kind != "added" {
		t.Fatalf("nil old should add everything: %v", ch)
	}
}
