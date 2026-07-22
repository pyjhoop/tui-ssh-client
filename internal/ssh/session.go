// Package ssh dials a server with golang.org/x/crypto/ssh, requests a PTY and
// starts a shell. Output bytes are published on a channel so the UI can pump
// them into its virtual terminal; nothing here knows about Bubble Tea.
package ssh

import (
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

// Session is a live shell on a remote host.
type Session struct {
	client *xssh.Client
	sess   *xssh.Session
	stdin  io.WriteCloser

	out chan []byte

	mu      sync.Mutex
	exitErr error
	closed  bool
}

// Connect dials the server and starts an interactive shell on a PTY of the
// given size. It blocks, so callers must run it off the UI goroutine.
func Connect(srv model.Server, cols, rows int) (*Session, error) {
	auth, err := authMethods(srv)
	if err != nil {
		return nil, err
	}

	cfg := &xssh.ClientConfig{
		User:    srv.User,
		Auth:    auth,
		Timeout: DialTimeout,
		// v0 does not verify host keys; known_hosts support is a v1 item.
		HostKeyCallback: xssh.InsecureIgnoreHostKey(), //nolint:gosec
	}

	client, err := xssh.Dial("tcp", srv.Addr(), cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", srv.Addr(), err)
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
		client: client,
		sess:   sess,
		stdin:  stdin,
		out:    make(chan []byte, 64),
	}

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

// Resize tells the remote about the new PTY geometry.
func (s *Session) Resize(cols, rows int) error {
	if cols < 1 || rows < 1 {
		return nil
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
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

func authMethods(srv model.Server) ([]xssh.AuthMethod, error) {
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
		}, nil

	case model.AuthKey:
		pem, err := os.ReadFile(srv.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("read key %s: %w", srv.KeyPath, err)
		}
		signer, err := xssh.ParsePrivateKey(pem)
		if err != nil {
			var passErr *xssh.PassphraseMissingError
			if errors.As(err, &passErr) {
				return nil, fmt.Errorf("key %s is passphrase-protected (not supported in v0)", srv.KeyPath)
			}
			return nil, fmt.Errorf("parse key %s: %w", srv.KeyPath, err)
		}
		return []xssh.AuthMethod{xssh.PublicKeys(signer)}, nil

	default:
		return nil, fmt.Errorf("unknown auth method %q", srv.Auth)
	}
}

func termType() string {
	if t := os.Getenv("TERM"); t != "" {
		return t
	}
	return "xterm-256color"
}
