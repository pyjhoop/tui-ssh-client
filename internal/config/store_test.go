package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
)

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s := config.New(t.TempDir())

	servers, err := s.Load()
	if err != nil {
		t.Fatalf("Load on empty dir: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("want no servers, got %d", len(servers))
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := config.New(t.TempDir())

	want := []model.Server{
		{ID: "a", Name: "prod", Host: "example.com", Port: 22, User: "root", Auth: model.AuthPassword, Password: "hunter2"},
		{ID: "b", Host: "10.0.0.4", Port: 2222, User: "deploy", Auth: model.AuthKey, KeyPath: "/keys/b.pem"},
	}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("want %d servers, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("server %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestAddAssignsIDAndDefaultPort(t *testing.T) {
	s := config.New(t.TempDir())

	saved, err := s.Add(model.Server{Host: "example.com", User: "root", Auth: model.AuthPassword, Password: "x"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if saved.ID == "" {
		t.Error("want a generated ID")
	}
	if saved.Port != model.DefaultPort {
		t.Errorf("want port %d, got %d", model.DefaultPort, saved.Port)
	}

	servers, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(servers) != 1 || servers[0].ID != saved.ID {
		t.Fatalf("Add did not persist the server: %+v", servers)
	}
}

func TestRemove(t *testing.T) {
	s := config.New(t.TempDir())
	if err := s.Save([]model.Server{{ID: "a", Host: "a"}, {ID: "b", Host: "b"}}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := s.Remove("a"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	servers, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(servers) != 1 || servers[0].ID != "b" {
		t.Fatalf("want only server b, got %+v", servers)
	}
}

func TestSaveKeyIs0600(t *testing.T) {
	s := config.New(t.TempDir())
	const body = "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"

	path, err := s.SaveKey("srv-1", body)
	if err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	if want := filepath.Join(s.KeysDir(), "srv-1.pem"); path != want {
		t.Errorf("path: got %q, want %q", path, want)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key permissions: got %04o, want 0600", perm)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if want := body + "\n"; string(got) != want {
		t.Errorf("key body: got %q, want %q", got, want)
	}
}

func TestSaveKeyRejectsEmptyBody(t *testing.T) {
	s := config.New(t.TempDir())
	if _, err := s.SaveKey("srv-1", "   \n"); err == nil {
		t.Fatal("want an error for an empty key body")
	}
}

func TestPlaintextWarningIsRecorded(t *testing.T) {
	s := config.New(t.TempDir())

	if s.PlaintextWarningSeen() {
		t.Fatal("warning should not be marked seen on a fresh config dir")
	}
	if err := s.MarkPlaintextWarningSeen(); err != nil {
		t.Fatalf("MarkPlaintextWarningSeen: %v", err)
	}
	if !s.PlaintextWarningSeen() {
		t.Fatal("warning should be marked seen after MarkPlaintextWarningSeen")
	}
}

func TestUpdateRoundTrip(t *testing.T) {
	s := config.New(t.TempDir())

	first, err := s.Add(model.Server{Name: "alpha", Host: "a.example", Port: 22, User: "root", Auth: model.AuthPassword, Password: "p"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	second, err := s.Add(model.Server{Name: "beta", Host: "b.example", Port: 22, User: "root", Auth: model.AuthPassword, Password: "p"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	first.Port = 2222
	first.Name = "alpha-edited"
	if err := s.Update(first); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 servers, got %d", len(got))
	}
	// The edited entry keeps its slot; editing must not reorder the sidebar.
	if got[0] != first {
		t.Errorf("updated entry: got %+v, want %+v", got[0], first)
	}
	if got[1].ID != second.ID {
		t.Errorf("second entry moved: got %+v", got[1])
	}

	if err := s.Update(model.Server{ID: "nope", Host: "x", User: "u"}); err == nil {
		t.Error("Update of an unknown ID should fail")
	}
}

// TestRemoveDeletesOnlyOurKeys is the safety property: we clean up keys/<id>.pem
// but never touch a key the user pointed us at.
func TestRemoveDeletesOnlyOurKeys(t *testing.T) {
	dir := t.TempDir()
	s := config.New(dir)

	ours, err := s.Add(model.Server{Host: "a", Port: 22, User: "u", Auth: model.AuthKey})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	ourKey, err := s.SaveKey(ours.ID, "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----")
	if err != nil {
		t.Fatalf("SaveKey: %v", err)
	}
	ours.KeyPath = ourKey
	if err := s.Update(ours); err != nil {
		t.Fatalf("Update: %v", err)
	}

	theirKey := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(theirKey, []byte("their key\n"), 0o600); err != nil {
		t.Fatalf("write their key: %v", err)
	}
	theirs, err := s.Add(model.Server{Host: "b", Port: 22, User: "u", Auth: model.AuthKey, KeyPath: theirKey})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := s.Remove(ours.ID); err != nil {
		t.Fatalf("Remove ours: %v", err)
	}
	if _, err := os.Stat(ourKey); !os.IsNotExist(err) {
		t.Errorf("keys/<id>.pem should be gone, stat gave %v", err)
	}

	if err := s.Remove(theirs.ID); err != nil {
		t.Fatalf("Remove theirs: %v", err)
	}
	if _, err := os.Stat(theirKey); err != nil {
		t.Errorf("a key outside KeysDir must survive removal: %v", err)
	}

	left, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("want an empty list, got %d entries", len(left))
	}
}

func TestAppendKnownHost(t *testing.T) {
	dir := t.TempDir()
	s := config.New(dir)

	if files := s.KnownHostsFiles(); contains(files, s.KnownHostsPath()) {
		t.Error("a known_hosts file that does not exist must not be listed")
	}

	if err := s.AppendKnownHost("[10.0.0.1]:22 ssh-ed25519 AAAA"); err != nil {
		t.Fatalf("AppendKnownHost: %v", err)
	}
	if err := s.AppendKnownHost("[10.0.0.2]:22 ssh-ed25519 BBBB\n"); err != nil {
		t.Fatalf("AppendKnownHost: %v", err)
	}

	b, err := os.ReadFile(s.KnownHostsPath())
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	want := "[10.0.0.1]:22 ssh-ed25519 AAAA\n[10.0.0.2]:22 ssh-ed25519 BBBB\n"
	if string(b) != want {
		t.Errorf("known_hosts: got %q, want %q", b, want)
	}

	info, err := os.Stat(s.KnownHostsPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("known_hosts mode: got %o, want 600", perm)
	}

	if !contains(s.KnownHostsFiles(), s.KnownHostsPath()) {
		t.Error("our known_hosts should be listed once it exists")
	}
}

// TestOwnsKey pins the boundary Remove relies on.
func TestOwnsKey(t *testing.T) {
	dir := t.TempDir()
	s := config.New(dir)

	if !s.OwnsKey(filepath.Join(s.KeysDir(), "abc.pem")) {
		t.Error("a path inside KeysDir is ours")
	}
	if s.OwnsKey(filepath.Join(dir, "servers.json")) {
		t.Error("a sibling of KeysDir is not ours")
	}
	if s.OwnsKey(filepath.Join(s.KeysDir(), "..", "..", "id_ed25519")) {
		t.Error("a path escaping KeysDir is not ours")
	}
	if s.OwnsKey("") {
		t.Error("an empty path is not ours")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
