package sftp_test

import (
	"os"
	"path/filepath"
	"testing"

	sftppkg "github.com/pyjhoop/tui-ssh-client/internal/sftp"
)

// TestLocalListSortsDirsFirst pins the ordering both panes depend on, plus the
// metadata the rows are drawn from.
func TestLocalListSortsDirsFirst(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "zebra.txt"), "hello")
	mustWrite(t, filepath.Join(dir, "Alpha.md"), "x")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "Docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	entries, err := sftppkg.Local{}.List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := []string{"Docs", "sub", "Alpha.md", "zebra.txt"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("got %v, want %v", names, want)
		}
	}

	// ".." is the UI's business, never the browser's.
	for _, e := range entries {
		if e.Name == ".." || e.Name == "." {
			t.Errorf("List must not emit %q", e.Name)
		}
	}

	if !entries[0].IsDir {
		t.Error("Docs should be reported as a directory")
	}
	if entries[3].Size != int64(len("hello")) {
		t.Errorf("zebra.txt size: got %d, want %d", entries[3].Size, len("hello"))
	}
}

func TestLocalPathHelpers(t *testing.T) {
	l := sftppkg.Local{}
	if got := l.Join("/a/b", "c"); got != filepath.Join("/a/b", "c") {
		t.Errorf("Join: got %q", got)
	}
	if got := l.Parent("/a/b"); got != filepath.Dir("/a/b") {
		t.Errorf("Parent: got %q", got)
	}
	// The root is its own parent — that is how the pane knows to drop "..".
	if root := filepath.Dir("/"); l.Parent(root) != root {
		t.Errorf("Parent(%q) should be itself", root)
	}
	if _, err := l.Home(); err != nil {
		t.Errorf("Home: %v", err)
	}
}

func TestLocalListMissingDir(t *testing.T) {
	if _, err := (sftppkg.Local{}).List(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("listing a missing directory should fail")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
