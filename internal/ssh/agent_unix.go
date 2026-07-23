//go:build !windows

package ssh

import (
	"fmt"
	"io"
	"net"
	"os"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// agentSigners connects to the agent named by SSH_AUTH_SOCK. There is no
// fallback to another method on purpose: silently authenticating some other way
// would leave the user unable to tell which credential was used.
//
// The connection is returned rather than closed here because the agent does the
// signing: it has to stay open for the whole handshake. Dial closes it with a
// defer once the handshake is over.
func agentSigners() (io.Closer, func() ([]xssh.Signer, error), error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, nil, fmt.Errorf("%w: SSH_AUTH_SOCK is not set", ErrAgentUnavailable)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: connect to %s: %w", ErrAgentUnavailable, sock, err)
	}
	return conn, agent.NewClient(conn).Signers, nil
}
