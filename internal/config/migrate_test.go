package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/model"
	"github.com/pyjhoop/tui-ssh-client/internal/vault"
)

// legacyConfig writes a pre-v6 servers.json by hand: model.Server no longer
// serialises a password, which is the point, so the fixture cannot be built
// through Save.
func legacyConfig(t *testing.T, dir string, body string) *config.Store {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	s := config.New(dir)
	if err := os.WriteFile(s.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrationMovesPlaintextAndDeletesIt(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "keys", "b.pem")
	s := legacyConfig(t, dir, `[
	  {"id":"a","host":"a.example","port":22,"user":"root","auth":"password","password":"hunter2"},
	  {"id":"b","host":"b.example","port":22,"user":"deploy","auth":"key","key_path":"`+escape(keyPath)+`"}
	]`)

	if _, err := s.SaveKey("b", "-----BEGIN OPENSSH PRIVATE KEY-----\nbody\n"); err != nil {
		t.Fatal(err)
	}

	pt, err := s.ScanPlaintext()
	if err != nil {
		t.Fatalf("ScanPlaintext: %v", err)
	}
	if !pt.Any() {
		t.Fatal("ScanPlaintext found nothing to migrate")
	}
	if pt.Passwords["a"] != "hunter2" {
		t.Errorf("password not found: %+v", pt.Passwords)
	}
	if !strings.Contains(pt.Keys["b"], "PRIVATE KEY") {
		t.Errorf("key body not found: %+v", pt.Keys)
	}

	sec := vault.Secrets{Version: vault.CurrentVersion}
	rep, err := s.Migrate(pt, &sec, testPass)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if rep.Passwords != 1 || rep.Keys != 1 {
		t.Errorf("report: %+v", rep)
	}

	// The secrets are in the vault…
	back, err := s.LoadSecrets(testPass)
	if err != nil {
		t.Fatalf("LoadSecrets: %v", err)
	}
	if back.Password("a") != "hunter2" || !strings.Contains(back.Keys["b"], "PRIVATE KEY") {
		t.Fatalf("vault does not hold the migrated secrets: %+v", back)
	}

	// …and gone from everywhere else.
	raw, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Errorf("servers.json still holds the password:\n%s", raw)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Errorf("keys/b.pem should be gone, stat gave %v", err)
	}
	entries, err := os.ReadDir(s.KeysDir())
	if err == nil && len(entries) != 0 {
		t.Errorf("keys/ should be empty, holds %d files", len(entries))
	}

	// The backup survives, readable only by its owner.
	if rep.BackupPath == "" {
		t.Fatal("no backup path reported")
	}
	wantPerm0600(t, rep.BackupPath, "backup")
	bak, err := os.ReadFile(rep.BackupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bak), "hunter2") {
		t.Errorf("the backup should be the original file:\n%s", bak)
	}

	// The entry that owned a key no longer points at a file that is not there.
	servers, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, srv := range servers {
		if srv.ID == "b" && srv.KeyPath != "" {
			t.Errorf("KeyPath should have been cleared, got %q", srv.KeyPath)
		}
	}

	// And the plaintext warning is retracted: it is no longer true.
	if s.PlaintextWarningSeen() {
		t.Error("the plaintext warning ack should have been removed")
	}
}

// TestMigrationLeavesUserKeysAlone is the OwnsKey rule: a key the user pointed
// us at belongs to them and to OpenSSH. Copying it into our vault would mean
// silently using a stale key the day they replace it.
func TestMigrationLeavesUserKeysAlone(t *testing.T) {
	theirDir := t.TempDir()
	theirKey := filepath.Join(theirDir, "id_ed25519")
	if err := os.WriteFile(theirKey, []byte("their private key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	s := legacyConfig(t, dir, `[
	  {"id":"a","host":"a.example","port":22,"user":"root","auth":"key","key_path":"`+escape(theirKey)+`"},
	  {"id":"b","host":"b.example","port":22,"user":"root","auth":"password","password":"pw"}
	]`)

	pt, err := s.ScanPlaintext()
	if err != nil {
		t.Fatal(err)
	}
	if len(pt.Keys) != 0 {
		t.Fatalf("a key outside KeysDir must not be collected: %+v", pt.Keys)
	}

	sec := vault.Secrets{Version: vault.CurrentVersion}
	if _, err := s.Migrate(pt, &sec, testPass); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := os.Stat(theirKey); err != nil {
		t.Errorf("their key file was touched: %v", err)
	}
	back, err := s.LoadSecrets(testPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Keys) != 0 {
		t.Errorf("their key ended up in the vault: %+v", back.Keys)
	}

	servers, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].KeyPath != theirKey {
		t.Errorf("their KeyPath was cleared: %q", servers[0].KeyPath)
	}
}

func TestNothingToMigrateIsANoOp(t *testing.T) {
	s := config.New(t.TempDir())
	if err := s.Save([]model.Server{{ID: "a", Host: "a", Port: 22, User: "u", Auth: model.AuthAgent}}); err != nil {
		t.Fatal(err)
	}

	pt, err := s.ScanPlaintext()
	if err != nil {
		t.Fatal(err)
	}
	if pt.Any() {
		t.Fatalf("nothing should need migrating: %+v", pt)
	}

	sec := vault.Secrets{}
	if _, err := s.Migrate(pt, &sec, testPass); err != nil {
		t.Fatal(err)
	}
	// No vault is created for a user who has no secrets — they must never be
	// made to invent a passphrase.
	if s.HasVault() {
		t.Error("a no-op migration created a vault")
	}
}

func TestInjectFillsSecretsForOneServer(t *testing.T) {
	var sec vault.Secrets
	sec.SetPassword("a", "pw")
	sec.SetKey("a", "pem body")
	sec.SetKeyPass("a", "kp")

	got := config.Inject(model.Server{ID: "a", Host: "h", User: "u"}, sec)
	if got.Password != "pw" || string(got.KeyPEM) != "pem body" || got.KeyPassphrase != "kp" {
		t.Fatalf("Inject: %+v", got)
	}

	other := config.Inject(model.Server{ID: "b"}, sec)
	if other.Password != "" || len(other.KeyPEM) != 0 || other.KeyPassphrase != "" {
		t.Fatalf("Inject leaked another server's secrets: %+v", other)
	}
}

// escape makes a Windows-style path safe to embed in a JSON fixture.
func escape(p string) string { return strings.ReplaceAll(p, `\`, `\\`) }
