package ssh_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// TestConnectPumpsOutputAndForwardsInput runs the real Connect path against an
// in-process SSH server that echoes whatever the client types.
func TestConnectPumpsOutputAndForwardsInput(t *testing.T) {
	srv := startTestServer(t)

	sess, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port,
		User: "tester", Auth: model.AuthPassword, Password: "secret",
	}, 80, 24, srv.trusted(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	if got := readUntil(t, sess, "banner"); !strings.Contains(got, "banner") {
		t.Fatalf("missing shell banner, got %q", got)
	}

	if _, err := sess.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := readUntil(t, sess, "hello"); !strings.Contains(got, "hello") {
		t.Fatalf("input was not echoed back, got %q", got)
	}

	if err := sess.Resize(100, 30); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	select {
	case dims := <-srv.resized:
		if dims != [2]int{100, 30} {
			t.Errorf("window-change: got %v, want [100 30]", dims)
		}
	case <-time.After(3 * time.Second):
		t.Error("server never saw the window-change request")
	}
}

func TestConnectRejectsBadPassword(t *testing.T) {
	srv := startTestServer(t)

	_, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port,
		User: "tester", Auth: model.AuthPassword, Password: "wrong",
	}, 80, 24, srv.trusted(t))
	if err == nil {
		t.Fatal("want an authentication error")
	}
}

// TestOutputChannelClosesOnRemoteExit covers the "remote shell exited" path the
// UI turns into sessionEndedMsg.
func TestOutputChannelClosesOnRemoteExit(t *testing.T) {
	srv := startTestServer(t)

	sess, err := sshpkg.Connect(model.Server{
		Host: srv.host, Port: srv.port,
		User: "tester", Auth: model.AuthPassword, Password: "secret",
	}, 80, 24, srv.trusted(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	// "exit" makes the test shell close the channel.
	if _, err := sess.Write([]byte("exit\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-sess.Output():
			if !ok {
				return // channel closed, as expected
			}
		case <-deadline:
			t.Fatal("output channel was never closed after the remote exited")
		}
	}
}

func readUntil(t *testing.T, sess *sshpkg.Session, want string) string {
	t.Helper()
	var buf strings.Builder
	deadline := time.After(5 * time.Second)
	for {
		select {
		case chunk, ok := <-sess.Output():
			if !ok {
				return buf.String()
			}
			buf.Write(chunk)
			if strings.Contains(buf.String(), want) {
				return buf.String()
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q, got %q", want, buf.String())
		}
	}
}

// ── in-process SSH server ───────────────────────────────────────────────────

type testServer struct {
	host    string
	port    int
	hostKey xssh.PublicKey
	resized chan [2]int
}

// addr is what the host key callback sees and what known_hosts lines key on.
func (s *testServer) addr() string { return net.JoinHostPort(s.host, strconv.Itoa(s.port)) }

// trusted returns Options with the server's own host key already on file, which
// is the "known and unchanged" path.
func (s *testServer) trusted(t *testing.T) sshpkg.Options {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(s.addr())}, s.hostKey)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return sshpkg.Options{KnownHostsFiles: []string{path}}
}

func startTestServer(t *testing.T) *testServer {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("host key signer: %v", err)
	}

	cfg := &xssh.ServerConfig{
		PasswordCallback: func(_ xssh.ConnMetadata, password []byte) (*xssh.Permissions, error) {
			if string(password) != "secret" {
				return nil, errors.New("bad password")
			}
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	srv := &testServer{
		host:    host,
		port:    port,
		hostKey: signer.PublicKey(),
		resized: make(chan [2]int, 4),
	}

	// Cleanups run last-in-first-out: close the listener first, then wait for
	// the accept loop and its connection handlers to drain.
	var wg sync.WaitGroup
	t.Cleanup(wg.Wait)
	t.Cleanup(func() { _ = ln.Close() })

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				srv.serve(conn, cfg)
			}()
		}
	}()

	return srv
}

func (s *testServer) serve(conn net.Conn, cfg *xssh.ServerConfig) {
	defer conn.Close()

	sc, chans, reqs, err := xssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go xssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(xssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			return
		}
		go s.handleRequests(chReqs)
		go s.echoShell(ch)
	}
}

func (s *testServer) handleRequests(reqs <-chan *xssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "pty-req", "shell":
			_ = req.Reply(true, nil)
		case "window-change":
			var dims struct{ Cols, Rows, W, H uint32 }
			if err := xssh.Unmarshal(req.Payload, &dims); err == nil {
				select {
				case s.resized <- [2]int{int(dims.Cols), int(dims.Rows)}:
				default:
				}
			}
			_ = req.Reply(true, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// echoShell stands in for a login shell: it prints a banner and echoes input
// until it sees "exit".
func (s *testServer) echoShell(ch xssh.Channel) {
	defer ch.Close()

	_, _ = io.WriteString(ch, "banner\r\n$ ")

	buf := make([]byte, 1024)
	for {
		n, err := ch.Read(buf)
		if n > 0 {
			line := string(buf[:n])
			if strings.Contains(line, "exit") {
				_, _ = ch.SendRequest("exit-status", false, xssh.Marshal(struct{ Status uint32 }{0}))
				return
			}
			_, _ = ch.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}
