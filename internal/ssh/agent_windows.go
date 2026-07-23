//go:build windows

package ssh

import (
	"fmt"
	"io"
	"os"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// windowsAgentPipe is where the OpenSSH agent service that ships with Windows
// listens. It is a named pipe, not a unix socket, which is the whole reason
// this file exists.
const windowsAgentPipe = `\\.\pipe\openssh-ssh-agent`

// agentSigners opens the OpenSSH agent the way Windows publishes it. Looking in
// the platform's own place is not a fallback to another credential: if the pipe
// is not there we still fail with ErrAgentUnavailable rather than quietly
// authenticating some other way, exactly as on unix.
//
// agent.NewClient wants an io.ReadWriter, not a net.Conn, so an *os.File over
// the pipe is enough — no third-party named-pipe dependency.
func agentSigners() (io.Closer, func() ([]xssh.Signer, error), error) {
	pipe := os.Getenv("SSH_AUTH_SOCK")
	if pipe == "" {
		pipe = windowsAgentPipe
	}
	f, err := os.OpenFile(pipe, os.O_RDWR, 0)
	if err != nil {
		// The path is in the message on purpose: "which agent did it look for"
		// is the first question when this fails.
		return nil, nil, fmt.Errorf("%w: open %s: %w", ErrAgentUnavailable, pipe, err)
	}
	return f, agent.NewClient(f).Signers, nil
}
