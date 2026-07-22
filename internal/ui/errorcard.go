package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// errorAdvice turns a failed connection into something actionable. It is the
// only place that maps causes to words, and it branches on sentinels via
// errors.Is — never on message text, so the ssh package can reword freely.
//
// The actions are what the card offers; a host key mismatch deliberately gets
// no retry, because retrying is not the answer to a key that changed.
func errorAdvice(err error, srv model.Server) (headline, hint string, actions []string) {
	retry := []string{"[r] retry", "[e] edit connection", "[esc] dismiss"}
	edit := []string{"[e] edit connection", "[esc] dismiss"}

	switch {
	case errors.Is(err, sshpkg.ErrAuth):
		return "authentication failed",
			"The password or key was rejected. Check the credentials for " + srv.User + ".",
			edit

	case errors.Is(err, sshpkg.ErrHostKeyMismatch):
		return "host key mismatch",
			"The server is offering a different key than the one on record.\n" +
				"This is what a machine-in-the-middle looks like. If you know the\n" +
				"host was rebuilt, delete its line from known_hosts by hand:\n" +
				firstLineOf(err),
			[]string{"[esc] dismiss"}

	case errors.Is(err, sshpkg.ErrHostKeyUnknown):
		return "host key not accepted",
			"The fingerprint was not approved, so the connection was refused.",
			retry

	case errors.Is(err, sshpkg.ErrTimeout):
		return "connection timed out",
			fmt.Sprintf("%s did not answer within %s. Check the address, the port and any firewall.",
				srv.Addr(), sshpkg.DialTimeout),
			retry

	case errors.Is(err, sshpkg.ErrUnreachable):
		return "host unreachable",
			"Could not reach " + srv.Addr() + ". Check the hostname and that sshd is listening.",
			retry

	case errors.Is(err, sshpkg.ErrSFTP):
		return "sftp unavailable",
			"The login worked, but " + srv.Addr() + " would not start the sftp\n" +
				"subsystem. That is a server-side setting (Subsystem sftp in\n" +
				"sshd_config), so retrying will not help. The terminal session\n" +
				"still works.",
			[]string{"[esc] dismiss"}

	case errors.Is(err, sshpkg.ErrConnectionLost):
		return "connection lost",
			"The session stopped answering and was dropped:\n" + firstLineOf(err) + "\n" +
				"Reconnecting opens a new shell — whatever was running is gone.",
			retry

	case errors.Is(err, sshpkg.ErrKeyFile):
		return "private key problem",
			"The key could not be read or parsed:\n" + firstLineOf(err),
			edit

	default:
		return "connection failed", firstLineOf(err), retry
	}
}

// firstLineOf keeps a wrapped error to one readable line so the card's layout
// stays predictable.
func firstLineOf(err error) string {
	if err == nil {
		return ""
	}
	return strings.SplitN(err.Error(), "\n", 2)[0]
}

// errorCard is the right panel's body after a failed connect.
func errorCard(err error, srv model.Server) string {
	headline, hint, actions := errorAdvice(err, srv)

	var b strings.Builder
	b.WriteString(styleError.Render("✗ " + headline))
	b.WriteString("\n  ")
	b.WriteString(styleHint.Render(fmt.Sprintf("%s@%s", srv.User, srv.Addr())))
	b.WriteString("\n\n")
	b.WriteString(hint)
	b.WriteString("\n\n")
	b.WriteString(styleHint.Render(strings.Join(actions, "   ")))
	return b.String()
}
