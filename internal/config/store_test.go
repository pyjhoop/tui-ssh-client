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
