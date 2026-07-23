package ssh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// newKey returns a fresh ed25519 key in both the forms the tests need.
func newKey(t *testing.T) (ed25519.PrivateKey, []byte) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := xssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	return priv, pem.EncodeToMemory(block)
}

// TestAgentAuth: an agent on SSH_AUTH_SOCK is enough on its own, and nothing is
// stored anywhere for it.
func TestAgentAuth(t *testing.T) {
	srv := startTestServer(t)
	priv, _ := newKey(t)

	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: priv}); err != nil {
		t.Fatalf("add key to keyring: %v", err)
	}
	startAgent(t, keyring)

	sess, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port, User: "tester", Auth: model.AuthAgent,
	}, 80, 24, srv.trusted(t))
	if err != nil {
		t.Fatalf("Connect with agent auth: %v", err)
	}
	defer sess.Close()

	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.sawKey.Load().(string); got != string(signer.PublicKey().Marshal()) {
		t.Error("the server did not see the agent's key")
	}
}

// TestMissingAgentIsErrAgentUnavailable: with no agent we fail loudly rather
// than quietly authenticating some other way. Which credential opened a session
// has to stay knowable.
func TestMissingAgentIsErrAgentUnavailable(t *testing.T) {
	srv := startTestServer(t)
	t.Setenv("SSH_AUTH_SOCK", "")

	_, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port, User: "tester", Auth: model.AuthAgent,
		// A password sitting in the entry must not be used as a fallback.
		Password: "secret",
	}, 80, 24, srv.trusted(t))
	if !errors.Is(err, sshpkg.ErrAgentUnavailable) {
		t.Fatalf("want ErrAgentUnavailable, got %v", err)
	}

	// A socket path that is not an agent fails the same way.
	t.Setenv("SSH_AUTH_SOCK", filepath.Join(t.TempDir(), "nothing.sock"))
	if _, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port, User: "tester", Auth: model.AuthAgent,
	}, 80, 24, srv.trusted(t)); !errors.Is(err, sshpkg.ErrAgentUnavailable) {
		t.Fatalf("want ErrAgentUnavailable for a dead socket, got %v", err)
	}
}

// TestKeyPEMFromVaultBeatsKeyPath: once a key has been migrated into the vault,
// a stale keys/<id>.pem left on disk must never be what gets used.
func TestKeyPEMFromVaultBeatsKeyPath(t *testing.T) {
	srv := startTestServer(t)

	vaultKey, vaultPEM := newKey(t)
	_, stalePEM := newKey(t)

	stalePath := filepath.Join(t.TempDir(), "stale.pem")
	if err := os.WriteFile(stalePath, stalePEM, 0o600); err != nil {
		t.Fatal(err)
	}

	sess, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port, User: "tester", Auth: model.AuthKey,
		KeyPEM: vaultPEM, KeyPath: stalePath,
	}, 80, 24, srv.trusted(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	signer, err := xssh.NewSignerFromKey(vaultKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.sawKey.Load().(string); got != string(signer.PublicKey().Marshal()) {
		t.Error("the key on disk won over the one from the vault")
	}
}

// TestKeyPathStillWorks: a key the user pointed us at is read from where they
// put it. Nothing about v6 changes that path.
func TestKeyPathStillWorks(t *testing.T) {
	srv := startTestServer(t)
	priv, pem := newKey(t)

	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem, 0o600); err != nil {
		t.Fatal(err)
	}

	sess, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port, User: "tester", Auth: model.AuthKey, KeyPath: path,
	}, 80, 24, srv.trusted(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := srv.sawKey.Load().(string); got != string(signer.PublicKey().Marshal()) {
		t.Error("the key at KeyPath was not the one offered")
	}
}

// TestEncryptedKeyUsesStoredPassphrase covers both halves of the locked-key
// path: without a passphrase we ask (a distinct sentinel, because the answer is
// a prompt and not an edit), and with the right one we connect.
func TestEncryptedKeyUsesStoredPassphrase(t *testing.T) {
	srv := startTestServer(t)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := xssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("key-pass"))
	if err != nil {
		t.Fatal(err)
	}
	locked := pem.EncodeToMemory(block)

	base := model.Server{
		Host: srv.host, Port: srv.port, User: "tester",
		Auth: model.AuthKey, KeyPEM: locked,
	}

	if _, err := sshpkg.Connect(base, 80, 24, srv.trusted(t)); !errors.Is(err, sshpkg.ErrKeyPassphraseRequired) {
		t.Fatalf("no passphrase: want ErrKeyPassphraseRequired, got %v", err)
	}

	wrong := base
	wrong.KeyPassphrase = "not it"
	if _, err := sshpkg.Connect(wrong, 80, 24, srv.trusted(t)); !errors.Is(err, sshpkg.ErrKeyPassphraseRequired) {
		t.Fatalf("wrong passphrase: want ErrKeyPassphraseRequired, got %v", err)
	}

	right := base
	right.KeyPassphrase = "key-pass"
	sess, err := sshpkg.Connect(right, 80, 24, srv.trusted(t))
	if err != nil {
		t.Fatalf("with the stored passphrase: %v", err)
	}
	sess.Close()
}

// startAgent serves keyring on a unix socket and points SSH_AUTH_SOCK at it.
func startAgent(t *testing.T, keyring agent.Agent) {
	t.Helper()
	// Unix socket paths are short by necessity; t.TempDir() can be long enough
	// to overflow sun_path on some systems, so keep the name minimal.
	sock := filepath.Join(t.TempDir(), "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = agent.ServeAgent(keyring, conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})

	t.Setenv("SSH_AUTH_SOCK", sock)
}
