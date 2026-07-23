package main

import (
	"runtime"
	"strings"
	"testing"
)

// TestBuildVersionFallback: with no ldflags the line is still complete and
// still identifies the binary. Test binaries carry no VCS stamps, so this
// pins what is always true — the shape and the platform half — rather than
// the commit, which only a real build has.
func TestBuildVersionFallback(t *testing.T) {
	got := buildVersion()

	if !strings.HasPrefix(got, "ssh-client ") {
		t.Errorf("version line does not name the program: %q", got)
	}
	for _, want := range []string{runtime.Version(), runtime.GOOS + "/" + runtime.GOARCH} {
		if !strings.Contains(got, want) {
			t.Errorf("version line %q is missing %q", got, want)
		}
	}
}

// TestBuildVersionUsesLdflags: what goreleaser injects is what gets printed,
// and the commit is shortened to the usual seven characters.
func TestBuildVersionUsesLdflags(t *testing.T) {
	old := [3]string{version, commit, date}
	defer func() { version, commit, date = old[0], old[1], old[2] }()

	version, commit, date = "v1.2.3", "abc1234def5678", "2026-07-23"

	got := buildVersion()
	want := "ssh-client v1.2.3 (abc1234, 2026-07-23, " + runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
