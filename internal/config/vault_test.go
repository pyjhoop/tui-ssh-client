package config_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/vault"
)

const testPass = "a decent passphrase"

func TestNoVaultMeansNothingToUnlock(t *testing.T) {
	s := config.New(t.TempDir())
	if s.HasVault() {
		t.Fatal("a fresh config dir must not claim to have a vault")
	}
	// A missing vault reads as empty secrets rather than an error: a user on
	// keys and ssh-agent alone never creates one.
	sec, err := s.LoadSecrets(testPass)
	if err != nil {
		t.Fatalf("LoadSecrets with no vault: %v", err)
	}
	if !sec.Empty() {
		t.Fatalf("want empty secrets, got %+v", sec)
	}
}

func TestVaultRoundTripAndFileIs0600(t *testing.T) {
	s := config.New(t.TempDir())

	var sec vault.Secrets
	sec.SetPassword("srv-1", "hunter2")
	sec.GitHub = &vault.GitHubAuth{Owner: "acme", Repo: "dotfiles", Path: "ssh-client.age", Token: "ghp_x"}
	if err := s.SaveSecrets(sec, testPass); err != nil {
		t.Fatalf("SaveSecrets: %v", err)
	}

	if !s.HasVault() {
		t.Fatal("HasVault should be true after a save")
	}
	wantPerm0600(t, s.VaultPath(), "vault")

	raw, err := os.ReadFile(s.VaultPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"hunter2", "ghp_x", "acme", "srv-1"} {
		if strings.Contains(string(raw), needle) {
			t.Errorf("vault.age contains %q in the clear", needle)
		}
	}

	back, err := s.LoadSecrets(testPass)
	if err != nil {
		t.Fatalf("LoadSecrets: %v", err)
	}
	if back.Password("srv-1") != "hunter2" || back.GitHub == nil || back.GitHub.Token != "ghp_x" {
		t.Fatalf("round trip lost data: %+v", back)
	}
	if back.Version != vault.CurrentVersion {
		t.Errorf("version: got %d, want %d", back.Version, vault.CurrentVersion)
	}
}

func TestWrongPassphraseIsDistinguishable(t *testing.T) {
	s := config.New(t.TempDir())
	var sec vault.Secrets
	sec.SetPassword("a", "pw")
	if err := s.SaveSecrets(sec, testPass); err != nil {
		t.Fatal(err)
	}

	_, err := s.LoadSecrets("not the passphrase")
	if !errors.Is(err, vault.ErrBadPassphrase) {
		t.Fatalf("want ErrBadPassphrase, got %v", err)
	}
}

// TestSaveSecretsIsAtomic: a failed write must leave the previous vault whole.
// Losing this file loses every password at once, so it is written through a tmp
// file and renamed, exactly like servers.json.
func TestSaveSecretsIsAtomic(t *testing.T) {
	dir := t.TempDir()
	s := config.New(dir)

	var first vault.Secrets
	first.SetPassword("a", "original")
	if err := s.SaveSecrets(first, testPass); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.VaultPath())
	if err != nil {
		t.Fatal(err)
	}

	// An empty passphrase fails inside Encrypt, before anything is written.
	var second vault.Secrets
	second.SetPassword("a", "replacement")
	if err := s.SaveSecrets(second, ""); err == nil {
		t.Fatal("saving under an empty passphrase should fail")
	}

	after, err := os.ReadFile(s.VaultPath())
	if err != nil {
		t.Fatalf("the vault vanished: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("a failed save replaced the vault")
	}
	if _, err := os.Stat(s.VaultPath() + ".tmp"); err == nil {
		t.Error("a temp file was left behind")
	}

	back, err := s.LoadSecrets(testPass)
	if err != nil || back.Password("a") != "original" {
		t.Fatalf("the original vault is no longer readable: %+v %v", back, err)
	}
}
