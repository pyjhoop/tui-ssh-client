package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pyjhoop/ssh-client/internal/vault"
)

const vaultFile = "vault.age"

// VaultPath is the encrypted secrets file. It sits beside servers.json: the
// list is meant to be editable by hand, the secrets are not readable at all.
func (s *Store) VaultPath() string { return filepath.Join(s.dir, vaultFile) }

// HasVault reports whether there is anything to unlock. A user on keys and
// ssh-agent alone never has one, and must never be asked for a passphrase.
func (s *Store) HasVault() bool {
	_, err := os.Stat(s.VaultPath())
	return err == nil
}

// LoadSecrets decrypts the vault. A missing file is empty secrets, not an
// error — the same rule Load follows for a missing servers.json.
func (s *Store) LoadSecrets(pass string) (vault.Secrets, error) {
	cipher, err := os.ReadFile(s.VaultPath())
	if errors.Is(err, os.ErrNotExist) {
		return vault.Secrets{Version: vault.CurrentVersion}, nil
	}
	if err != nil {
		return vault.Secrets{}, fmt.Errorf("read %s: %w", s.VaultPath(), err)
	}
	plain, err := vault.Decrypt(cipher, pass)
	if err != nil {
		// ErrBadPassphrase passes through unwrapped-in-meaning: the UI branches
		// on it to decide whether asking again could possibly help.
		return vault.Secrets{}, err
	}
	var sec vault.Secrets
	if err := json.Unmarshal(plain, &sec); err != nil {
		return vault.Secrets{}, fmt.Errorf("parse vault contents: %w", err)
	}
	if sec.Version == 0 {
		sec.Version = vault.CurrentVersion
	}
	return sec, nil
}

// SaveSecrets seals the vault and replaces it atomically, 0600, exactly the way
// Save writes servers.json. A crash mid-write leaves the previous vault intact;
// losing the file would lose every password at once.
func (s *Store) SaveSecrets(sec vault.Secrets, pass string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	if sec.Version == 0 {
		sec.Version = vault.CurrentVersion
	}
	plain, err := json.Marshal(sec)
	if err != nil {
		return fmt.Errorf("encode secrets: %w", err)
	}
	cipher, err := vault.Encrypt(plain, pass)
	if err != nil {
		return err
	}

	tmp := s.VaultPath() + ".tmp"
	if err := os.WriteFile(tmp, cipher, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	// WriteFile respects umask on creation, so force the mode explicitly.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.VaultPath()); err != nil {
		return fmt.Errorf("replace %s: %w", s.VaultPath(), err)
	}
	return nil
}
