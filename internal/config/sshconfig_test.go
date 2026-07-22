package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pyjhoop/ssh-client/internal/model"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestParsesHostBlocks(t *testing.T) {
	path := writeConfig(t, `
# a comment
Host web-1
    HostName 10.0.0.1
    User deploy
    Port 2222
    IdentityFile /keys/web.pem

Host db-1
    hostname=10.0.0.2
    USER=postgres
    ServerAliveInterval 30
`)

	entries, err := ParseSSHConfig(path)
	if err != nil {
		t.Fatalf("ParseSSHConfig: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}

	want := []SSHConfigEntry{
		{Alias: "web-1", Host: "10.0.0.1", User: "deploy", Port: 2222, Identity: "/keys/web.pem"},
		{Alias: "db-1", Host: "10.0.0.2", User: "postgres"},
	}
	for i, w := range want {
		if entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, entries[i], w)
		}
	}

	// Port 0 becomes the default only once the entry is turned into a server.
	if got := entries[1].Server().Port; got != model.DefaultPort {
		t.Errorf("db-1 port = %d, want %d", got, model.DefaultPort)
	}
}

func TestWildcardAndIncludeAreSkipped(t *testing.T) {
	path := writeConfig(t, `
Host *
    User root

Include ~/.ssh/conf.d/*

Host web-?
    HostName wild

Match host bastion
    User jump

Host keeper
    HostName 10.0.0.9
    User me
`)

	entries, err := ParseSSHConfig(path)
	if err != nil {
		t.Fatalf("ParseSSHConfig: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5: %+v", len(entries), entries)
	}
	for i, e := range entries[:4] {
		if !e.Skip {
			t.Errorf("entry %d (%q) should be skipped", i, e.Alias)
		}
		if e.Reason == "" {
			t.Errorf("entry %d (%q) is skipped with no reason", i, e.Alias)
		}
	}

	// The real host after them still parses: one unsupported keyword must not
	// cost the user the rest of their config.
	last := entries[4]
	if last.Skip || last.Host != "10.0.0.9" || last.User != "me" {
		t.Errorf("keeper = %+v, want a parsed entry", last)
	}
}

func TestMissingFileIsNotAnError(t *testing.T) {
	entries, err := ParseSSHConfig(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("missing file returned %v, want nil", err)
	}
	if entries != nil {
		t.Errorf("missing file returned %+v, want nil", entries)
	}
}

func TestIdentityTildeExpanded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	path := writeConfig(t, "Host h\n  HostName x\n  IdentityFile ~/.ssh/id_ed25519\n")

	entries, err := ParseSSHConfig(path)
	if err != nil {
		t.Fatalf("ParseSSHConfig: %v", err)
	}
	want := filepath.Join(home, ".ssh", "id_ed25519")
	if entries[0].Identity != want {
		t.Errorf("Identity = %q, want %q", entries[0].Identity, want)
	}
	// An identity turns the entry into key auth, pointing at the user's own
	// file — nothing is copied into keys/.
	srv := entries[0].Server()
	if srv.Auth != model.AuthKey || srv.KeyPath != want {
		t.Errorf("Server() = auth %q path %q, want key auth at %q", srv.Auth, srv.KeyPath, want)
	}
}

func TestHostNameDefaultsToAlias(t *testing.T) {
	path := writeConfig(t, "Host example.com\n  User me\n")

	entries, err := ParseSSHConfig(path)
	if err != nil {
		t.Fatalf("ParseSSHConfig: %v", err)
	}
	if entries[0].Skip {
		t.Fatalf("entry was skipped: %+v", entries[0])
	}
	if entries[0].Host != "example.com" {
		t.Errorf("Host = %q, want the alias", entries[0].Host)
	}
}

func TestEntryWithoutIdentityUsesPasswordAuth(t *testing.T) {
	path := writeConfig(t, "Host h\n  HostName 10.0.0.1\n  User me\n")

	entries, err := ParseSSHConfig(path)
	if err != nil {
		t.Fatalf("ParseSSHConfig: %v", err)
	}
	srv := entries[0].Server()
	if srv.Auth != model.AuthPassword || srv.Password != "" {
		t.Errorf("Server() = %+v, want empty password auth", srv)
	}
	if srv.Group != ImportGroup {
		t.Errorf("Group = %q, want %q", srv.Group, ImportGroup)
	}
}
