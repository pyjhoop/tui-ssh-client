package sftp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sftppkg "github.com/pyjhoop/ssh-client/internal/sftp"
)

// TestPlanWalksATree pins what the confirmation panel and the progress bar are
// both built on: the file count, the byte total, and the ordering that lets a
// copy create a directory before writing into it.
func TestPlanWalksATree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "proj")
	mkdirs(t, filepath.Join(root, "pkg", "deep"), filepath.Join(root, "empty"))
	mustWrite(t, filepath.Join(root, "top.txt"), "12345")
	mustWrite(t, filepath.Join(root, "pkg", "a.go"), "aa")
	mustWrite(t, filepath.Join(root, "pkg", "deep", "b.go"), "b")

	items, total, skipped, err := sftppkg.Plan(sftppkg.Local{}, root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if total != 8 {
		t.Errorf("total = %d, want 8", total)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}

	var files, dirs int
	seen := map[string]int{}
	for i, it := range items {
		seen[it.RelPath] = i
		if it.IsDir {
			dirs++
		} else {
			files++
		}
	}
	if files != 3 || dirs != 4 { // the root, pkg, pkg/deep, empty
		t.Errorf("got %d files and %d directories, want 3 and 4: %+v", files, dirs, items)
	}
	if items[0].RelPath != "" || !items[0].IsDir {
		t.Errorf("the first item should be the root directory, got %+v", items[0])
	}

	// Every item's parent comes before it, or a copy would write into a
	// directory that does not exist yet.
	for rel, idx := range seen {
		parent := ""
		if i := strings.LastIndex(rel, "/"); i >= 0 {
			parent = rel[:i]
		}
		if rel == "" {
			continue
		}
		if pidx, ok := seen[parent]; !ok || pidx > idx {
			t.Errorf("%q (at %d) comes before its parent %q (at %d)", rel, idx, parent, pidx)
		}
	}
}

// TestPlanSkipsSymlinks is the reason Plan stats without following: a link back
// to its own parent is a cycle, and walking it would never end.
func TestPlanSkipsSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loop")
	mkdirs(t, filepath.Join(root, "sub"))
	mustWrite(t, filepath.Join(root, "real.txt"), "xyz")
	if err := os.Symlink(root, filepath.Join(root, "sub", "up")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// If the cycle were followed this call would never return, and the test
	// binary's own timeout is what would report it.
	items, total, skipped, err := sftppkg.Plan(sftppkg.Local{}, root)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (the directory link and the file link)", skipped)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 — only the real file counts", total)
	}
	for _, it := range items {
		if strings.HasSuffix(it.RelPath, "up") || strings.HasSuffix(it.RelPath, "link.txt") {
			t.Errorf("a symlink made it into the plan: %+v", it)
		}
	}
}

// TestPlanOfASingleFile: the same call has to work for the common case, with
// the root itself as the only item.
func TestPlanOfASingleFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "one.txt")
	mustWrite(t, p, "hello")

	items, total, skipped, err := sftppkg.Plan(sftppkg.Local{}, p)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) != 1 || items[0].IsDir || items[0].RelPath != "" || items[0].Size != 5 {
		t.Errorf("items = %+v, want one 5-byte file at the root", items)
	}
	if total != 5 || skipped != 0 {
		t.Errorf("total=%d skipped=%d, want 5 and 0", total, skipped)
	}

	if _, _, _, err := sftppkg.Plan(sftppkg.Local{}, p+".missing"); err == nil {
		t.Error("planning a missing path should fail")
	}
}
