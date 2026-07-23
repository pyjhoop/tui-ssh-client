package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pyjhoop/ssh-client/internal/model"
	"github.com/pyjhoop/ssh-client/internal/vault"
)

// plaintextBackupFile is the copy left behind before the cleartext secrets are
// removed. It is 0600 and the status line names it, because a migration the
// user did not expect must not be the thing that loses their passwords.
//
// Deleting a file does not scrub it: on a journalling filesystem or an SSD the
// old blocks may survive until they are reused, and that is not something this
// program can fix. It is documented rather than pretended away.
const plaintextBackupFile = "servers.json.plaintext.bak"

// Plaintext is what a pre-v6 config still holds in the clear. It is gathered
// before anything is written, so the caller can ask for a passphrase first and
// still know there is a reason to.
type Plaintext struct {
	Passwords map[string]string // serverID → password from servers.json
	Keys      map[string]string // serverID → pem body of a key we generated
	KeyFiles  map[string]string // serverID → the keys/<id>.pem path to delete
}

// Any reports whether there is anything to migrate at all.
func (p Plaintext) Any() bool { return len(p.Passwords) > 0 || len(p.Keys) > 0 }

// MigrateReport is the summary the status line shows.
type MigrateReport struct {
	Passwords  int
	Keys       int
	BackupPath string
}

// legacyServer reads only the fields v6 took out of servers.json. It exists
// because model.Server no longer serialises them: after the migration the
// program must have no way to read a password off disk.
type legacyServer struct {
	ID       string `json:"id"`
	Password string `json:"password"`
	KeyPath  string `json:"key_path"`
}

// ScanPlaintext finds secrets still living in the clear: passwords in
// servers.json, and private keys under keys/.
//
// Only keys we generated are collected. A KeyPath pointing at ~/.ssh/id_ed25519
// belongs to the user and OpenSSH; pulling it into our vault would mean quietly
// using a stale copy the day they replace it. OwnsKey is the same rule v1 used
// to decide what Remove may delete.
func (s *Store) ScanPlaintext() (Plaintext, error) {
	pt := Plaintext{
		Passwords: map[string]string{},
		Keys:      map[string]string{},
		KeyFiles:  map[string]string{},
	}

	b, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return pt, nil
	}
	if err != nil {
		return pt, fmt.Errorf("read %s: %w", s.Path(), err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return pt, nil
	}

	var legacy []legacyServer
	if err := json.Unmarshal(b, &legacy); err != nil {
		return pt, fmt.Errorf("parse %s: %w", s.Path(), err)
	}

	for _, l := range legacy {
		if l.ID == "" {
			continue
		}
		if l.Password != "" {
			pt.Passwords[l.ID] = l.Password
		}
		if !s.OwnsKey(l.KeyPath) {
			continue
		}
		body, err := os.ReadFile(l.KeyPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return pt, fmt.Errorf("read %s: %w", l.KeyPath, err)
		}
		pt.Keys[l.ID] = string(body)
		pt.KeyFiles[l.ID] = l.KeyPath
	}
	return pt, nil
}

// Migrate moves the cleartext secrets into the vault and then removes them.
//
// The order is the whole of the safety here: the vault is sealed and on disk
// *before* servers.json is rewritten and the pem files are unlinked. Reversing
// it would mean a crash in the middle loses the secrets outright.
func (s *Store) Migrate(pt Plaintext, sec *vault.Secrets, pass string) (MigrateReport, error) {
	var rep MigrateReport
	if !pt.Any() {
		return rep, nil
	}
	if sec == nil {
		return rep, errors.New("migrate: no secrets to migrate into")
	}

	// 1. Keep a copy of what is about to be destroyed.
	raw, err := os.ReadFile(s.Path())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return rep, fmt.Errorf("read %s: %w", s.Path(), err)
	}
	if len(raw) > 0 {
		backup := s.backupPath(plaintextBackupFile)
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return rep, fmt.Errorf("write %s: %w", backup, err)
		}
		if err := os.Chmod(backup, 0o600); err != nil {
			return rep, fmt.Errorf("chmod %s: %w", backup, err)
		}
		rep.BackupPath = backup
	}

	// 2. Fill the vault and seal it. Nothing is deleted until this succeeds.
	for id, pw := range pt.Passwords {
		sec.SetPassword(id, pw)
	}
	for id, pem := range pt.Keys {
		sec.SetKey(id, pem)
	}
	if err := s.SaveSecrets(*sec, pass); err != nil {
		return rep, err
	}
	rep.Passwords, rep.Keys = len(pt.Passwords), len(pt.Keys)

	// 3. Rewrite the list without the secrets. Password no longer serialises at
	//    all, so this is just a re-save; KeyPath has to be cleared by hand for
	//    the keys that moved, or it would point at a file we are about to unlink.
	servers, err := s.Load()
	if err != nil {
		return rep, err
	}
	for i := range servers {
		if _, moved := pt.Keys[servers[i].ID]; moved {
			servers[i].KeyPath = ""
		}
	}
	if err := s.Save(servers); err != nil {
		return rep, err
	}

	// 4. Now the originals can go.
	for _, path := range pt.KeyFiles {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return rep, fmt.Errorf("remove %s: %w", path, err)
		}
	}
	// The plaintext warning is no longer true, so it should not be on record as
	// having been shown.
	_ = os.Remove(filepath.Join(s.dir, warnFile))

	return rep, nil
}

// Inject fills a server's secret fields from the vault, just before it is
// dialled. It is the only place the two halves are put back together: the list
// on disk knows the host, the vault knows how to get in.
func Inject(srv model.Server, sec vault.Secrets) model.Server {
	srv.Password = sec.Passwords[srv.ID]
	if pem, ok := sec.Keys[srv.ID]; ok {
		srv.KeyPEM = []byte(pem)
	}
	srv.KeyPassphrase = sec.KeyPass[srv.ID]
	return srv
}
