// Package vault encrypts a byte slice under a passphrase using age's scrypt
// recipient. It owns no files and knows no paths: config decides where the
// ciphertext lives, ui decides when to ask for the passphrase.
//
// The primitives are deliberately not assembled here. age is an audited format
// with a twenty-line API, and hand-rolling Argon2id + XChaCha20-Poly1305 would
// mean owning KDF parameters and nonce management for no gain. Nothing in this
// package may grow its own crypto.
package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"filippo.io/age"
)

// ErrBadPassphrase is the one failure the UI must tell apart, because it is the
// only one the user can fix by typing again.
var ErrBadPassphrase = errors.New("wrong passphrase")

// ErrEmptyPassphrase guards the caller that would otherwise seal a vault under
// nothing at all.
var ErrEmptyPassphrase = errors.New("passphrase is empty")

// MinPassphraseLen is the shortest passphrase we will create a vault with. The
// strength of this one string is the whole of the design's security, so a
// too-short one is refused rather than warned about.
const MinPassphraseLen = 8

// scryptWorkFactor is age's log2 work factor. This runs once at startup, not
// per keystroke, so it is set well above the library default of 18.
const scryptWorkFactor = 19

// Encrypt seals plain under passphrase. The work factor is deliberately high:
// this runs once at startup, not per keystroke.
func Encrypt(plain []byte, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrEmptyPassphrase
	}
	rec, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("build recipient: %w", err)
	}
	rec.SetWorkFactor(scryptWorkFactor)

	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rec)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	if _, err := w.Write(plain); err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	return buf.Bytes(), nil
}

// Decrypt returns ErrBadPassphrase for a wrong passphrase — the one error the
// UI must distinguish, because it is the only one the user can fix by retrying.
//
// age reports "no identity matched any of the recipients" for a bad passphrase,
// which for a scrypt file can mean nothing else: there is exactly one identity
// and one recipient. Anything else is a damaged or truncated file.
func Decrypt(cipher []byte, passphrase string) ([]byte, error) {
	if passphrase == "" {
		return nil, ErrEmptyPassphrase
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("build identity: %w", err)
	}

	r, err := age.Decrypt(bytes.NewReader(cipher), id)
	if err != nil {
		if errors.Is(err, age.ErrIncorrectIdentity) || isNoMatch(err) {
			return nil, ErrBadPassphrase
		}
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plain, nil
}

// isNoMatch recognises age's aggregate "no identity matched" failure. age wraps
// the per-identity errors in an unexported type, so this is the one place that
// falls back to the message — and it stays behind ErrBadPassphrase, which is
// what the rest of the program matches on.
func isNoMatch(err error) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte("no identity matched"))
}

// Secrets is everything we must never write unencrypted.
type Secrets struct {
	Version   int               `json:"version"`
	Passwords map[string]string `json:"passwords,omitempty"` // serverID → password
	Keys      map[string]string `json:"keys,omitempty"`      // serverID → private key PEM
	KeyPass   map[string]string `json:"key_pass,omitempty"`  // serverID → key passphrase
	GitHub    *GitHubAuth       `json:"github,omitempty"`    // sync token, only once opted in
}

// GitHubAuth is the sync registration: where the bundle lives and the token
// that may write it. It sits inside the vault because a token is a secret like
// any other, and because a new machine gets its repo coordinates from the pull.
type GitHubAuth struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Token  string `json:"token"`
	SHA    string `json:"sha,omitempty"` // last blob sha we saw: the optimistic lock
}

// CurrentVersion is the Secrets format we write. It is checked after decryption
// so a later format can be migrated in the clear, in memory.
const CurrentVersion = 1

// Password returns the stored password for a server, if any.
func (s Secrets) Password(id string) string { return s.Passwords[id] }

// SetPassword stores (or clears) a server's password.
func (s *Secrets) SetPassword(id, pw string) {
	if pw == "" {
		delete(s.Passwords, id)
		return
	}
	if s.Passwords == nil {
		s.Passwords = map[string]string{}
	}
	s.Passwords[id] = pw
}

// SetKey stores (or clears) a server's private key body.
func (s *Secrets) SetKey(id, pem string) {
	if pem == "" {
		delete(s.Keys, id)
		return
	}
	if s.Keys == nil {
		s.Keys = map[string]string{}
	}
	s.Keys[id] = pem
}

// SetKeyPass stores (or clears) the passphrase that unlocks a server's key.
func (s *Secrets) SetKeyPass(id, pass string) {
	if pass == "" {
		delete(s.KeyPass, id)
		return
	}
	if s.KeyPass == nil {
		s.KeyPass = map[string]string{}
	}
	s.KeyPass[id] = pass
}

// Forget drops every secret belonging to a deleted server.
func (s *Secrets) Forget(id string) {
	delete(s.Passwords, id)
	delete(s.Keys, id)
	delete(s.KeyPass, id)
}

// Empty reports whether there is nothing worth encrypting. A vault is only
// created once there is: users on keys and ssh-agent alone must never be made
// to invent a passphrase.
func (s Secrets) Empty() bool {
	return len(s.Passwords) == 0 && len(s.Keys) == 0 && len(s.KeyPass) == 0 && s.GitHub == nil
}
