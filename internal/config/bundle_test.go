package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
	"github.com/pyjhoop/ssh-client/internal/vault"
)

// Two host key lines for the same host, differing only in the key. That
// disagreement is what a machine-in-the-middle looks like, so it must never be
// resolved silently.
const (
	lineA    = "a.example ssh-ed25519 AAAAKEYONE"
	lineAlt  = "a.example ssh-ed25519 AAAAKEYTWO"
	lineB    = "b.example ssh-ed25519 AAAAKEYTHREE"
	lineOnly = "c.example ssh-ed25519 AAAAKEYFOUR"
)

func TestBundleRoundTrip(t *testing.T) {
	src := config.New(t.TempDir())
	if err := src.Save([]model.Server{
		{ID: "1", Name: "web", Host: "a.example", Port: 22, User: "deploy", Auth: model.AuthPassword},
		{ID: "2", Name: "db", Host: "b.example", Port: 2222, User: "root", Auth: model.AuthAgent},
	}); err != nil {
		t.Fatal(err)
	}
	if err := src.AppendKnownHost(lineA); err != nil {
		t.Fatal(err)
	}

	var sec vault.Secrets
	sec.SetPassword("1", "hunter2")

	blob, err := src.Bundle(sec)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	n, _, device, err := config.BundleServers(blob)
	if err != nil {
		t.Fatalf("BundleServers: %v", err)
	}
	if n != 2 {
		t.Errorf("bundle says %d servers, want 2", n)
	}
	_ = device

	// Apply it into a machine that has never seen any of this.
	dst := config.New(t.TempDir())
	var got vault.Secrets
	rep, err := dst.ApplyBundle(blob, &got)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}
	if rep.Servers != 2 {
		t.Errorf("report says %d servers", rep.Servers)
	}
	if got.Password("1") != "hunter2" {
		t.Errorf("secrets did not come across: %+v", got)
	}

	servers, err := dst.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers[0].Name != "web" || servers[1].Auth != model.AuthAgent {
		t.Fatalf("server list did not come across: %+v", servers)
	}
	known, err := os.ReadFile(dst.KnownHostsPath())
	if err != nil {
		t.Fatalf("known_hosts: %v", err)
	}
	if !strings.Contains(string(known), lineA) {
		t.Errorf("known_hosts missing the bundled line:\n%s", known)
	}
}

// TestApplyMergesKnownHosts: a pull replaces the server list wholesale but must
// never replace known_hosts. Hosts approved only on this machine have to
// survive, or the next connection re-prompts for a fingerprint — which trains
// the user to wave them through.
func TestApplyMergesKnownHosts(t *testing.T) {
	src := config.New(t.TempDir())
	if err := src.Save([]model.Server{{ID: "1", Host: "a.example", Port: 22, User: "u"}}); err != nil {
		t.Fatal(err)
	}
	for _, l := range []string{lineA, lineB} {
		if err := src.AppendKnownHost(l); err != nil {
			t.Fatal(err)
		}
	}
	blob, err := src.Bundle(vault.Secrets{})
	if err != nil {
		t.Fatal(err)
	}

	dst := config.New(t.TempDir())
	// This machine already trusts lineA (a duplicate) and lineOnly (local only).
	for _, l := range []string{lineA, lineOnly} {
		if err := dst.AppendKnownHost(l); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := dst.ApplyBundle(blob, nil)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}
	if rep.KnownHostsNew != 1 {
		t.Errorf("added %d lines, want 1 (lineB)", rep.KnownHostsNew)
	}

	raw, err := os.ReadFile(dst.KnownHostsPath())
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, l := range []string{lineA, lineB, lineOnly} {
		if !strings.Contains(body, l) {
			t.Errorf("union is missing %q:\n%s", l, body)
		}
	}
	if strings.Count(body, lineA) != 1 {
		t.Errorf("the duplicate line was added twice:\n%s", body)
	}
}

// TestConflictingHostKeyKeepsLocal: same host, different key. Local wins and
// the report names it, because choosing quietly is the failure mode.
func TestConflictingHostKeyKeepsLocal(t *testing.T) {
	src := config.New(t.TempDir())
	if err := src.Save([]model.Server{{ID: "1", Host: "a.example", Port: 22, User: "u"}}); err != nil {
		t.Fatal(err)
	}
	if err := src.AppendKnownHost(lineAlt); err != nil {
		t.Fatal(err)
	}
	blob, err := src.Bundle(vault.Secrets{})
	if err != nil {
		t.Fatal(err)
	}

	dst := config.New(t.TempDir())
	if err := dst.AppendKnownHost(lineA); err != nil {
		t.Fatal(err)
	}

	rep, err := dst.ApplyBundle(blob, nil)
	if err != nil {
		t.Fatalf("ApplyBundle: %v", err)
	}
	if len(rep.Conflicts) != 1 || rep.Conflicts[0] != "a.example" {
		t.Fatalf("conflicts: got %v, want [a.example]", rep.Conflicts)
	}

	raw, err := os.ReadFile(dst.KnownHostsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "AAAAKEYTWO") {
		t.Errorf("the remote key overwrote the local one:\n%s", raw)
	}
	if !strings.Contains(string(raw), "AAAAKEYONE") {
		t.Errorf("the local key was lost:\n%s", raw)
	}
}

// TestApplyBackupsTheLocalList: a pull the user did not mean must be undoable
// without going back to the remote.
func TestApplyBackupsTheLocalList(t *testing.T) {
	src := config.New(t.TempDir())
	if err := src.Save([]model.Server{{ID: "9", Name: "remote-only", Host: "z", Port: 22, User: "u"}}); err != nil {
		t.Fatal(err)
	}
	blob, err := src.Bundle(vault.Secrets{})
	if err != nil {
		t.Fatal(err)
	}

	dst := config.New(t.TempDir())
	if err := dst.Save([]model.Server{{ID: "1", Name: "local-only", Host: "y", Port: 22, User: "u"}}); err != nil {
		t.Fatal(err)
	}

	rep, err := dst.ApplyBundle(blob, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.BackupPath == "" {
		t.Fatal("no backup was recorded")
	}
	raw, err := os.ReadFile(rep.BackupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !strings.Contains(string(raw), "local-only") {
		t.Errorf("backup does not hold the replaced list:\n%s", raw)
	}
}
