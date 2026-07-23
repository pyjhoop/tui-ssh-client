package vault

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	sec := Secrets{
		Version:   CurrentVersion,
		Passwords: map[string]string{"a": "hunter2"},
		Keys:      map[string]string{"b": "-----BEGIN PRIVATE KEY-----\n"},
	}
	plain, err := json.Marshal(sec)
	if err != nil {
		t.Fatal(err)
	}

	cipher, err := Encrypt(plain, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(cipher, "correct horse battery staple")
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}

	var back Secrets
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if back.Passwords["a"] != "hunter2" || back.Keys["b"] == "" {
		t.Fatalf("round trip lost data: %+v", back)
	}
}

func TestWrongPassphraseIsErrBadPassphrase(t *testing.T) {
	cipher, err := Encrypt([]byte("secret"), "right one")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Decrypt(cipher, "wrong one")
	if !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("want ErrBadPassphrase, got %v", err)
	}
}

func TestEmptyPassphraseIsRefused(t *testing.T) {
	if _, err := Encrypt([]byte("x"), ""); !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("Encrypt with empty passphrase: %v", err)
	}
	if _, err := Decrypt([]byte("x"), ""); !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("Decrypt with empty passphrase: %v", err)
	}
}

// TestCiphertextLeaksNothing is the point of the whole package: not one byte of
// what went in may be readable in what comes out, host names included.
func TestCiphertextLeaksNothing(t *testing.T) {
	sec := Secrets{
		Version:   CurrentVersion,
		Passwords: map[string]string{"srv-1": "s3cr3t-pw"},
		GitHub:    &GitHubAuth{Owner: "acme", Repo: "dotfiles", Token: "ghp_deadbeef"},
	}
	plain, err := json.Marshal(sec)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := Encrypt(plain, "a passphrase")
	if err != nil {
		t.Fatal(err)
	}

	for _, needle := range []string{"s3cr3t-pw", "ghp_deadbeef", "acme", "dotfiles", "srv-1", "passwords"} {
		if bytes.Contains(cipher, []byte(needle)) {
			t.Errorf("ciphertext contains %q in the clear", needle)
		}
	}
}

func TestDamagedCiphertextIsNotABadPassphrase(t *testing.T) {
	_, err := Decrypt([]byte("this is not an age file"), "whatever")
	if err == nil {
		t.Fatal("want an error")
	}
	if errors.Is(err, ErrBadPassphrase) {
		t.Fatal("a corrupt file must not look like a retryable passphrase")
	}
}

func TestEmptyReportsWhetherAVaultIsWorthMaking(t *testing.T) {
	var s Secrets
	if !s.Empty() {
		t.Fatal("zero Secrets should be empty")
	}
	s.SetPassword("a", "pw")
	if s.Empty() {
		t.Fatal("a stored password is not empty")
	}
	s.SetPassword("a", "")
	if !s.Empty() {
		t.Fatal("clearing the last secret should be empty again")
	}
	s.GitHub = &GitHubAuth{}
	if s.Empty() {
		t.Fatal("a sync registration is a secret too")
	}
}

func TestForgetDropsEverySecretForAServer(t *testing.T) {
	var s Secrets
	s.SetPassword("a", "pw")
	s.SetKey("a", "pem")
	s.SetKeyPass("a", "kp")
	s.SetPassword("b", "other")

	s.Forget("a")
	if len(s.Passwords) != 1 || s.Passwords["b"] != "other" {
		t.Fatalf("Forget touched the wrong server: %+v", s)
	}
	if len(s.Keys) != 0 || len(s.KeyPass) != 0 {
		t.Fatalf("Forget left something behind: %+v", s)
	}
}
