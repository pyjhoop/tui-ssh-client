package ssh

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Sentinel causes for a failed connection. The UI branches on these with
// errors.Is and never on message text, so wording can change freely.
var (
	ErrAuth            = errors.New("authentication failed")
	ErrUnreachable     = errors.New("host unreachable")
	ErrTimeout         = errors.New("connection timed out")
	ErrHostKeyUnknown  = errors.New("host key not accepted")
	ErrHostKeyMismatch = errors.New("host key mismatch")
	ErrKeyFile         = errors.New("private key problem")
	// ErrSFTP means the connection itself was fine but the server would not
	// start the sftp subsystem — a server-side configuration problem, not
	// something retrying or editing the entry can fix.
	ErrSFTP = errors.New("sftp subsystem unavailable")
	// ErrConnectionLost is a session that died under us: the keepalive got no
	// answer, or the transport dropped. It is deliberately distinct from a clean
	// remote exit, because only this one may be reconnected automatically —
	// retrying an "exit" would be an endless loop.
	ErrConnectionLost = errors.New("connection lost")
	// ErrAgentUnavailable means auth was set to "agent" and there is no agent to
	// ask. We deliberately do not fall back to a password or a key file: the user
	// would no longer know which credential opened the session.
	ErrAgentUnavailable = errors.New("ssh-agent unavailable")
	// ErrKeyPassphraseRequired is a private key we can read but not decrypt,
	// because its passphrase is not in the vault yet. The UI answers it with a
	// one-line prompt and stores the answer, so it is asked once per key.
	ErrKeyPassphraseRequired = errors.New("key passphrase required")
)

// classify wraps a raw dial error with the sentinel that says what to do about
// it. Errors we already tagged (the host key callback's) pass through.
func classify(err error) error {
	if err == nil {
		return nil
	}
	for _, sentinel := range []error{ErrHostKeyMismatch, ErrHostKeyUnknown, ErrAgentUnavailable, ErrKeyPassphraseRequired, ErrKeyFile, ErrAuth, ErrTimeout, ErrUnreachable, ErrConnectionLost} {
		if errors.Is(err, sentinel) {
			return err
		}
	}

	// knownhosts errors can surface from the handshake without passing through
	// our callback's own wrapping (a callback we did not install, say).
	var revoked *knownhosts.RevokedError
	if errors.As(err, &revoked) {
		return join(err, ErrHostKeyMismatch)
	}
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) > 0 {
			return join(err, ErrHostKeyMismatch)
		}
		return join(err, ErrHostKeyUnknown)
	}

	// Timeout before unreachable: a dial timeout is also a *net.OpError.
	if os.IsTimeout(err) || errors.Is(err, os.ErrDeadlineExceeded) {
		return join(err, ErrTimeout)
	}

	var partial *xssh.PartialSuccessError
	if errors.As(err, &partial) {
		return join(err, ErrAuth)
	}
	// x/crypto reports a rejected handshake as a plain error string; there is no
	// typed authentication failure to match on, so this one case reads the text.
	if strings.Contains(err.Error(), "unable to authenticate") ||
		strings.Contains(err.Error(), "no supported methods remain") {
		return join(err, ErrAuth)
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return join(err, ErrUnreachable)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return join(err, ErrUnreachable)
	}
	return err
}

// join keeps the original message readable while making the cause matchable.
// Both errors stay in the chain, so errors.Is finds the sentinel and the
// underlying net/ssh error is still visible in the text.
func join(err, sentinel error) error {
	return fmt.Errorf("%w: %w", sentinel, err)
}
