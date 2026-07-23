//go:build windows

package ssh_test

import (
	"errors"
	"testing"

	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// TestMissingAgentIsErrAgentUnavailable is the Windows half of the v6 rule: no
// agent means a loud failure, never a quiet fallback to the password sitting in
// the entry.
//
// It points SSH_AUTH_SOCK at a pipe that does not exist rather than clearing
// it, because clearing it falls back to the machine's real OpenSSH agent, which
// may or may not be running on any given host.
func TestMissingAgentIsErrAgentUnavailable(t *testing.T) {
	srv := startTestServer(t)
	t.Setenv("SSH_AUTH_SOCK", `\\.\pipe\ssh-client-no-such-agent`)

	_, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port, User: "tester", Auth: model.AuthAgent,
		Password: "secret",
	}, 80, 24, srv.trusted(t))
	if !errors.Is(err, sshpkg.ErrAgentUnavailable) {
		t.Fatalf("want ErrAgentUnavailable, got %v", err)
	}
}
