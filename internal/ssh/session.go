// Package ssh dials a server with golang.org/x/crypto/ssh, requests a PTY and
// starts a shell. Output bytes are published on a channel so the UI can pump
// them into its virtual terminal; nothing here knows about Bubble Tea.
package ssh

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/pyjhoop/ssh-client/internal/model"
)

// DialTimeout bounds the TCP + handshake phase.
const DialTimeout = 15 * time.Second

// readChunk is the read buffer size for the stdout pump.
const readChunk = 32 * 1024

// KeepaliveInterval is how often we poke the server, and also how long we wait
// for the answer. Without it a connection that dies silently (laptop asleep, VPN
// dropped, NAT entry evicted) just leaves the read goroutine blocked forever:
// nothing ever arrives, so nothing ever reports the session as gone.
const KeepaliveInterval = 30 * time.Second

// keepaliveRequest is OpenSSH's no-op global request. Any answer at all — even
// a refusal — proves the transport is alive.
const keepaliveRequest = "keepalive@openssh.com"

// Session is a live shell on a remote host.
type Session struct {
	client *xssh.Client
	sess   *xssh.Session
	stdin  io.WriteCloser

	out chan []byte

	mu      sync.Mutex
	exitErr error
	closed  bool
	// Last geometry we sent, so a drag-resize storm does not turn into one
	// window-change request per pixel.
	sentCols, sentRows int
}

// Dial opens an authenticated client connection, verifying the host key against
// opts. It is the single entry point to the network: Connect builds a shell on
// top of it, and v2's SFTP support will reuse it as-is.
func Dial(srv model.Server, opts Options) (*xssh.Client, error) {
	auth, agentConn, err := authMethods(srv)
	if err != nil {
		return nil, err
	}
	defer agentConn.Close()

	hostKey, err := opts.hostKeyCallback()
	if err != nil {
		return nil, err
	}

	cfg := &xssh.ClientConfig{
		User:            srv.User,
		Auth:            auth,
		Timeout:         DialTimeout,
		HostKeyCallback: hostKey,
	}

	client, err := xssh.Dial("tcp", srv.Addr(), cfg)
	if err != nil {
		return nil, classify(fmt.Errorf("connect to %s: %w", srv.Addr(), err))
	}
	return client, nil
}

// Connect dials the server and starts an interactive shell on a PTY of the
// given size. It blocks, so callers must run it off the UI goroutine.
func Connect(srv model.Server, cols, rows int, opts Options) (*Session, error) {
	client, err := Dial(srv, opts)
	if err != nil {
		return nil, err
	}

	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("open session: %w", err)
	}

	modes := xssh.TerminalModes{
		xssh.ECHO:          1,
		xssh.TTY_OP_ISPEED: 14400,
		xssh.TTY_OP_OSPEED: 14400,
	}
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	if err := sess.RequestPty(termType(), rows, cols, modes); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("request pty: %w", err)
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	// With a PTY the remote merges stderr into stdout, but ask for it anyway so
	// pre-shell errors are not lost.
	stderr, err := sess.StderrPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := sess.Shell(); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return nil, fmt.Errorf("start shell: %w", err)
	}

	s := &Session{
		client:   client,
		sess:     sess,
		stdin:    stdin,
		out:      make(chan []byte, 64),
		sentCols: cols,
		sentRows: rows,
	}

	go s.keepalive(opts.keepaliveInterval())

	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() { defer pumps.Done(); s.pump(stdout) }()
	go func() { defer pumps.Done(); s.pump(stderr) }()

	go func() {
		// Wait for the remote shell to exit, then let the readers drain before
		// closing the channel so the UI sees the final bytes.
		err := sess.Wait()
		pumps.Wait()
		s.mu.Lock()
		if s.exitErr == nil && !s.closed {
			s.exitErr = err
		}
		s.mu.Unlock()
		s.finish()
	}()

	return s, nil
}

// Output yields raw terminal bytes. It is closed when the session ends.
func (s *Session) Output() <-chan []byte { return s.out }

// ExitErr is the reason the session ended, or nil for a clean exit.
func (s *Session) ExitErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitErr
}

// Write forwards key bytes to the remote shell's stdin.
func (s *Session) Write(p []byte) (int, error) {
	n, err := s.stdin.Write(p)
	if err != nil {
		return n, fmt.Errorf("write to session: %w", err)
	}
	return n, nil
}

// Resize tells the remote about the new PTY geometry. Sizes it has already
// been told are dropped: dragging a window edge produces a burst of identical
// WindowSizeMsgs, and each request is a round trip.
func (s *Session) Resize(cols, rows int) error {
	if cols < 1 || rows < 1 {
		return nil
	}
	s.mu.Lock()
	closed := s.closed
	unchanged := s.sentCols == cols && s.sentRows == rows
	if !closed && !unchanged {
		s.sentCols, s.sentRows = cols, rows
	}
	s.mu.Unlock()
	if closed || unchanged {
		return nil
	}
	if err := s.sess.WindowChange(rows, cols); err != nil {
		return fmt.Errorf("window change: %w", err)
	}
	return nil
}

// Close tears the session down. It is safe to call more than once.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	_ = s.stdin.Close()
	_ = s.sess.Close()
	err := s.client.Close()
	if err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("close client: %w", err)
	}
	return nil
}

// keepalive proves the connection is still there, and ends the session with
// ErrConnectionLost when it is not.
//
// SendRequest has no deadline of its own and blocks on a dead transport until
// TCP gives up, which can take minutes, so the reply is raced against a timer.
// The stray goroutine that is still waiting for the answer ends when fail
// closes the client under it.
func (s *Session) keepalive(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for range ticker.C {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}

		answered := make(chan error, 1)
		go func() {
			_, _, err := s.client.SendRequest(keepaliveRequest, true, nil)
			answered <- err
		}()

		select {
		case err := <-answered:
			if err != nil {
				s.fail(fmt.Errorf("%w: %w", ErrConnectionLost, err))
				return
			}
		case <-time.After(every):
			s.fail(fmt.Errorf("%w: no keepalive reply within %s", ErrConnectionLost, every))
			return
		}
	}
}

// fail records why the session is dying and then drops the transport, which
// wakes the readers so the normal end-of-session path runs. The recorded error
// survives: finish only fills exitErr in when it is still nil.
func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.exitErr == nil {
		s.exitErr = err
	}
	s.mu.Unlock()
	_ = s.client.Close()
}

func (s *Session) pump(r io.Reader) {
	buf := make([]byte, readChunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if !s.send(chunk) {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// send publishes a chunk, reporting false once the session is done.
func (s *Session) send(chunk []byte) bool {
	defer func() {
		// The channel is closed by finish(); losing the race is not fatal.
		_ = recover()
	}()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return false
	}
	s.out <- chunk
	return true
}

func (s *Session) finish() {
	s.mu.Lock()
	already := s.closed
	s.closed = true
	s.mu.Unlock()
	if !already {
		_ = s.stdin.Close()
		_ = s.sess.Close()
		_ = s.client.Close()
	}
	close(s.out)
}

// authMethods builds the credentials for one dial. The io.Closer is the agent
// socket when there is one and a no-op otherwise; Dial closes it once the
// handshake is over, because an agent connection lives exactly as long as the
// signing it is there to do.
func authMethods(srv model.Server) ([]xssh.AuthMethod, io.Closer, error) {
	switch srv.Auth {
	case model.AuthPassword:
		return []xssh.AuthMethod{
			xssh.Password(srv.Password),
			// Many servers offer keyboard-interactive instead of password.
			xssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = srv.Password
				}
				return answers, nil
			}),
		}, noopCloser{}, nil

	case model.AuthKey:
		signer, err := keySigner(srv)
		if err != nil {
			return nil, noopCloser{}, err
		}
		return []xssh.AuthMethod{xssh.PublicKeys(signer)}, noopCloser{}, nil

	case model.AuthAgent:
		conn, signers, err := agentSigners()
		if err != nil {
			return nil, noopCloser{}, err
		}
		return []xssh.AuthMethod{xssh.PublicKeysCallback(signers)}, conn, nil

	default:
		return nil, noopCloser{}, fmt.Errorf("%w: unknown auth method %q", ErrKeyFile, srv.Auth)
	}
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// keySigner turns the entry's key into a signer. A key body from the vault wins
// over KeyPath: since v6 a key the user pasted lives encrypted, and reading a
// stale keys/<id>.pem left over from before the migration would be worse than
// failing.
func keySigner(srv model.Server) (xssh.Signer, error) {
	pem := srv.KeyPEM
	source := "the stored key"
	if len(pem) == 0 {
		if srv.KeyPath == "" {
			return nil, fmt.Errorf("%w: no key body and no key path", ErrKeyFile)
		}
		b, err := os.ReadFile(srv.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("%w: read key %s: %w", ErrKeyFile, srv.KeyPath, err)
		}
		pem, source = b, srv.KeyPath
	}

	signer, err := xssh.ParsePrivateKey(pem)
	if err == nil {
		return signer, nil
	}

	var passErr *xssh.PassphraseMissingError
	if !errors.As(err, &passErr) {
		return nil, fmt.Errorf("%w: parse key %s: %w", ErrKeyFile, source, err)
	}
	// Locked key. The passphrase comes from the vault; without one, say so with
	// the sentinel that makes the UI ask for it rather than the generic key
	// failure, which offers only "edit the entry".
	if srv.KeyPassphrase == "" {
		return nil, fmt.Errorf("%w: %s is passphrase-protected", ErrKeyPassphraseRequired, source)
	}
	signer, err = xssh.ParsePrivateKeyWithPassphrase(pem, []byte(srv.KeyPassphrase))
	if err != nil {
		// A wrong stored passphrase is the same situation as a missing one: ask
		// again. x/crypto reports it as x509's incorrect-password error for both
		// the OpenSSH and the legacy PEM formats.
		if errors.Is(err, x509.IncorrectPasswordError) {
			return nil, fmt.Errorf("%w: the stored passphrase does not open %s", ErrKeyPassphraseRequired, source)
		}
		return nil, fmt.Errorf("%w: parse key %s: %w", ErrKeyFile, source, err)
	}
	return signer, nil
}

func termType() string {
	if t := os.Getenv("TERM"); t != "" {
		return t
	}
	return "xterm-256color"
}
