package ssh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

func TestHostKeyKnownKeyConnects(t *testing.T) {
	srv := startTestServer(t)

	sess, err := sshpkg.Connect(testCreds(srv), 80, 24, srv.trusted(t))
	if err != nil {
		t.Fatalf("Connect with a matching known_hosts entry: %v", err)
	}
	_ = sess.Close()
}

// TestHostKeyMismatchIsRefused pins the rule that matters most: a changed key
// is an error with no approval path, not a prompt.
func TestHostKeyMismatchIsRefused(t *testing.T) {
	srv := startTestServer(t)

	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(srv.addr())}, otherHostKey(t))
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	prompts := make(chan *sshpkg.HostKeyPrompt, 1)
	_, err := sshpkg.Connect(testCreds(srv), 80, 24, sshpkg.Options{
		KnownHostsFiles: []string{path},
		AppendKnownHost: func(string) error { return nil },
		Prompts:         prompts,
	})
	if !errors.Is(err, sshpkg.ErrHostKeyMismatch) {
		t.Fatalf("got %v, want ErrHostKeyMismatch", err)
	}
	select {
	case p := <-prompts:
		t.Fatalf("a changed key must never be offered for approval, got a prompt for %s", p.Addr)
	default:
	}
}

// TestHostKeyTOFUAccept covers the whole trust-on-first-use round trip: the
// callback blocks on the dialing goroutine, the UI answers, and the approved
// line lands in our file.
func TestHostKeyTOFUAccept(t *testing.T) {
	srv := startTestServer(t)

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create known_hosts: %v", err)
	}

	prompts := make(chan *sshpkg.HostKeyPrompt, 1)
	opts := sshpkg.Options{
		KnownHostsFiles: []string{path},
		AppendKnownHost: func(line string) error {
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = f.WriteString(line + "\n")
			return err
		},
		Prompts: prompts,
	}

	type result struct {
		sess *sshpkg.Session
		err  error
	}
	done := make(chan result, 1)
	go func() {
		sess, err := sshpkg.Connect(testCreds(srv), 80, 24, opts)
		done <- result{sess, err}
	}()

	var prompt *sshpkg.HostKeyPrompt
	select {
	case prompt = <-prompts:
	case <-time.After(5 * time.Second):
		t.Fatal("no fingerprint prompt for an unknown host")
	}
	if !strings.HasPrefix(prompt.Fingerprint, "SHA256:") {
		t.Errorf("fingerprint %q is not a SHA256 fingerprint", prompt.Fingerprint)
	}
	if prompt.Addr != srv.addr() {
		t.Errorf("prompt addr: got %q, want %q", prompt.Addr, srv.addr())
	}
	prompt.Accept()

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Connect after approval: %v", r.err)
		}
		_ = r.sess.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("Connect never returned after the prompt was accepted")
	}

	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if !strings.Contains(string(saved), prompt.Line) {
		t.Errorf("approved key was not appended; file is %q", saved)
	}

	// A second connect must not ask again.
	select {
	case p := <-prompts:
		t.Fatalf("asked again for an already-approved host: %s", p.Addr)
	default:
	}
	sess, err := sshpkg.Connect(testCreds(srv), 80, 24, opts)
	if err != nil {
		t.Fatalf("reconnect to an approved host: %v", err)
	}
	_ = sess.Close()
}

func TestHostKeyTOFURejectFails(t *testing.T) {
	srv := startTestServer(t)

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create known_hosts: %v", err)
	}

	prompts := make(chan *sshpkg.HostKeyPrompt, 1)
	done := make(chan error, 1)
	go func() {
		_, err := sshpkg.Connect(testCreds(srv), 80, 24, sshpkg.Options{
			KnownHostsFiles: []string{path},
			AppendKnownHost: func(string) error { return nil },
			Prompts:         prompts,
		})
		done <- err
	}()

	select {
	case p := <-prompts:
		p.Reject()
	case <-time.After(5 * time.Second):
		t.Fatal("no fingerprint prompt")
	}

	select {
	case err := <-done:
		if !errors.Is(err, sshpkg.ErrHostKeyUnknown) {
			t.Fatalf("got %v, want ErrHostKeyUnknown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect never returned after the prompt was rejected")
	}
}

// TestHostKeyWithoutPromptChannelFailsClosed guards the zero-value Options: no
// way to ask means refuse, never trust.
func TestHostKeyWithoutPromptChannelFailsClosed(t *testing.T) {
	srv := startTestServer(t)

	_, err := sshpkg.Connect(testCreds(srv), 80, 24, sshpkg.Options{})
	if !errors.Is(err, sshpkg.ErrHostKeyUnknown) {
		t.Fatalf("got %v, want ErrHostKeyUnknown", err)
	}
}

func testCreds(srv *testServer) model.Server {
	return model.Server{
		Host: srv.host, Port: srv.port,
		User: "tester", Auth: model.AuthPassword, Password: "secret",
	}
}

func otherHostKey(t *testing.T) xssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer.PublicKey()
}
