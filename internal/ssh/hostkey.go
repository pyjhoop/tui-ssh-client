package ssh

import (
	"errors"
	"fmt"
	"net"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyPromptTimeout bounds how long the handshake waits for the user. The
// callback runs on the dialing goroutine; without a bound, an app that quits
// mid-prompt would leak it forever.
const HostKeyPromptTimeout = 60 * time.Second

// HostKeyPrompt is a trust-on-first-use question handed to the UI. The dialing
// goroutine blocks inside the host key callback until Accept or Reject is
// called, which is why the UI must never answer it from a blocking context.
type HostKeyPrompt struct {
	Addr        string // the dial target, host:port
	Fingerprint string // ssh.FingerprintSHA256 of the offered key
	KeyType     string // "ssh-ed25519", "ecdsa-sha2-nistp256", …
	Line        string // the known_hosts line that Accept would append

	reply chan bool
}

// Accept trusts the key: it is appended to our known_hosts and the handshake
// continues. Extra calls are ignored.
func (p *HostKeyPrompt) Accept() { p.answer(true) }

// Reject aborts the handshake with ErrHostKeyUnknown.
func (p *HostKeyPrompt) Reject() { p.answer(false) }

func (p *HostKeyPrompt) answer(ok bool) {
	select {
	case p.reply <- ok:
	default: // already answered, or nobody is waiting any more
	}
}

// Options carries everything Dial needs beyond the server entry itself. The
// zero value verifies against no files at all and offers no way to approve a
// key, so it fails closed rather than trusting whatever answers.
type Options struct {
	// KnownHostsFiles are read for verification. Every path must exist;
	// config.Store.KnownHostsFiles filters missing ones out.
	KnownHostsFiles []string

	// AppendKnownHost persists an approved key. Nil disables trust-on-first-use.
	AppendKnownHost func(line string) error

	// Prompts receives trust-on-first-use questions. Nil disables them.
	//
	// An automatic reconnect deliberately leaves this nil: approving a new host
	// key must be something the user is looking at when it happens, so an
	// unattended retry fails with ErrHostKeyUnknown instead of asking.
	Prompts chan<- *HostKeyPrompt

	// Keepalive overrides KeepaliveInterval. Zero means the default; tests set
	// it short so a dead connection can be detected without waiting 30 seconds.
	Keepalive time.Duration
}

func (o Options) keepaliveInterval() time.Duration {
	if o.Keepalive > 0 {
		return o.Keepalive
	}
	return KeepaliveInterval
}

// hostKeyCallback builds the verification callback. Its three outcomes are:
// known key → nil, changed key → ErrHostKeyMismatch (never approvable), unknown
// key → ask, and on refusal ErrHostKeyUnknown.
func (o Options) hostKeyCallback() (xssh.HostKeyCallback, error) {
	known, err := knownhosts.New(o.KnownHostsFiles...)
	if err != nil {
		return nil, fmt.Errorf("read known_hosts: %w", err)
	}

	return func(hostname string, remote net.Addr, key xssh.PublicKey) error {
		err := known(hostname, remote, key)
		if err == nil {
			return nil
		}

		// A revoked key is a hard no, same as a changed one.
		var revoked *knownhosts.RevokedError
		if errors.As(err, &revoked) {
			return fmt.Errorf("%w: %s offered a revoked key", ErrHostKeyMismatch, hostname)
		}

		var keyErr *knownhosts.KeyError
		if !errors.As(err, &keyErr) {
			return err
		}
		if len(keyErr.Want) > 0 {
			// Deliberately not approvable from the UI: this is exactly what a
			// machine-in-the-middle looks like, and a one-keystroke override
			// would make the whole check pointless. The user edits the file.
			return fmt.Errorf("%w: %s offers %s %s, but %s is on record",
				ErrHostKeyMismatch, hostname, key.Type(), xssh.FingerprintSHA256(key),
				describeKnown(keyErr.Want))
		}

		return o.trustOnFirstUse(hostname, key)
	}, nil
}

func (o Options) trustOnFirstUse(hostname string, key xssh.PublicKey) error {
	if o.Prompts == nil || o.AppendKnownHost == nil {
		return fmt.Errorf("%w: %s is not in known_hosts", ErrHostKeyUnknown, hostname)
	}

	prompt := &HostKeyPrompt{
		Addr:        hostname,
		Fingerprint: xssh.FingerprintSHA256(key),
		KeyType:     key.Type(),
		Line:        knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key),
		reply:       make(chan bool, 1),
	}

	timeout := time.NewTimer(HostKeyPromptTimeout)
	defer timeout.Stop()

	select {
	case o.Prompts <- prompt:
	case <-timeout.C:
		return fmt.Errorf("%w: nobody was listening for the fingerprint prompt", ErrHostKeyUnknown)
	}

	select {
	case accepted := <-prompt.reply:
		if !accepted {
			return fmt.Errorf("%w: %s was not trusted", ErrHostKeyUnknown, hostname)
		}
	case <-timeout.C:
		return fmt.Errorf("%w: the fingerprint prompt timed out", ErrHostKeyUnknown)
	}

	if err := o.AppendKnownHost(prompt.Line); err != nil {
		return fmt.Errorf("%w: %w", ErrHostKeyUnknown, err)
	}
	return nil
}

// describeKnown summarises the keys we already have on file, including where
// they came from — the user has to go edit exactly that line.
func describeKnown(want []knownhosts.KnownKey) string {
	if len(want) == 0 {
		return "another key"
	}
	k := want[0]
	return fmt.Sprintf("%s %s (%s:%d)",
		k.Key.Type(), xssh.FingerprintSHA256(k.Key), k.Filename, k.Line)
}
