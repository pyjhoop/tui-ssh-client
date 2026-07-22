package ssh_test

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// The UI branches on these sentinels with errors.Is. Nothing here compares
// message text, so the ssh package stays free to reword its errors.

func TestConnectErrorsAreTyped(t *testing.T) {
	srv := startTestServer(t)

	t.Run("bad password is ErrAuth", func(t *testing.T) {
		creds := testCreds(srv)
		creds.Password = "wrong"
		_, err := sshpkg.Connect(creds, 80, 24, srv.trusted(t))
		if !errors.Is(err, sshpkg.ErrAuth) {
			t.Fatalf("got %v, want ErrAuth", err)
		}
	})

	t.Run("closed port is ErrUnreachable", func(t *testing.T) {
		creds := testCreds(srv)
		creds.Port = closedPort(t)
		_, err := sshpkg.Connect(creds, 80, 24, srv.trusted(t))
		if !errors.Is(err, sshpkg.ErrUnreachable) {
			t.Fatalf("got %v, want ErrUnreachable", err)
		}
	})

	t.Run("unresolvable host is ErrUnreachable", func(t *testing.T) {
		creds := testCreds(srv)
		creds.Host = "no-such-host.invalid"
		_, err := sshpkg.Connect(creds, 80, 24, srv.trusted(t))
		if !errors.Is(err, sshpkg.ErrUnreachable) {
			t.Fatalf("got %v, want ErrUnreachable", err)
		}
	})

	t.Run("missing key file is ErrKeyFile", func(t *testing.T) {
		creds := testCreds(srv)
		creds.Auth = model.AuthKey
		creds.Password = ""
		creds.KeyPath = filepath.Join(t.TempDir(), "absent.pem")
		_, err := sshpkg.Connect(creds, 80, 24, srv.trusted(t))
		if !errors.Is(err, sshpkg.ErrKeyFile) {
			t.Fatalf("got %v, want ErrKeyFile", err)
		}
	})

	t.Run("unparseable key is ErrKeyFile", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "junk.pem")
		if err := os.WriteFile(path, []byte("not a key\n"), 0o600); err != nil {
			t.Fatalf("write key: %v", err)
		}
		creds := testCreds(srv)
		creds.Auth = model.AuthKey
		creds.Password = ""
		creds.KeyPath = path
		_, err := sshpkg.Connect(creds, 80, 24, srv.trusted(t))
		if !errors.Is(err, sshpkg.ErrKeyFile) {
			t.Fatalf("got %v, want ErrKeyFile", err)
		}
	})
}

// closedPort returns a port nothing is listening on.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return port
}
