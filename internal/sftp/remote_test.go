package sftp_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	pkgsftp "github.com/pkg/sftp"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/pyjhoop/ssh-client/internal/model"
	sftppkg "github.com/pyjhoop/ssh-client/internal/sftp"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// TestRemoteRoundTrip covers the whole transfer path against an in-process SFTP
// server: this machine has no sshd, so the subsystem is served by pkg/sftp
// itself. It is modelled on internal/ssh/session_test.go's harness.
func TestRemoteRoundTrip(t *testing.T) {
	srv := startSFTPServer(t)
	remoteDir := t.TempDir()
	localDir := t.TempDir()

	r, err := sftppkg.Connect(srv.server(), srv.trusted(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer r.Close()

	if got := r.Label(); got != "tester@127.0.0.1" {
		t.Errorf("Label: got %q", got)
	}
	if _, err := r.Home(); err != nil {
		t.Errorf("Home: %v", err)
	}

	// ── listing ─────────────────────────────────────────────────────────────
	mustWrite(t, filepath.Join(remoteDir, "b.txt"), "bee")
	if err := os.Mkdir(filepath.Join(remoteDir, "adir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries, err := r.List(remoteDir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 || entries[0].Name != "adir" || !entries[0].IsDir {
		t.Fatalf("List: got %+v, want the directory first", entries)
	}
	if entries[1].Name != "b.txt" || entries[1].Size != 3 {
		t.Errorf("List: got %+v", entries[1])
	}

	// ── upload ──────────────────────────────────────────────────────────────
	src := filepath.Join(localDir, "up.txt")
	mustWrite(t, src, "uploaded body")
	dst := path.Join(remoteDir, "up.txt")

	if _, ok, err := r.Stat(dst); err != nil || ok {
		t.Fatalf("Stat before upload: ok=%v err=%v, want (false, nil)", ok, err)
	}
	n, err := sftppkg.Upload(r, src, dst)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if n != int64(len("uploaded body")) {
		t.Errorf("Upload copied %d bytes, want %d", n, len("uploaded body"))
	}
	if got := mustRead(t, dst); got != "uploaded body" {
		t.Errorf("uploaded content: got %q", got)
	}
	if _, ok, err := r.Stat(dst); err != nil || !ok {
		t.Errorf("Stat after upload: ok=%v err=%v, want (true, nil)", ok, err)
	}

	// ── download ────────────────────────────────────────────────────────────
	mustWrite(t, filepath.Join(remoteDir, "down.txt"), "downloaded body")
	localDst := filepath.Join(localDir, "down.txt")
	if _, ok, err := sftppkg.StatLocal(localDst); err != nil || ok {
		t.Fatalf("StatLocal before download: ok=%v err=%v", ok, err)
	}
	if _, err := sftppkg.Download(r, path.Join(remoteDir, "down.txt"), localDst); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := mustRead(t, localDst); got != "downloaded body" {
		t.Errorf("downloaded content: got %q", got)
	}
	if _, ok, err := sftppkg.StatLocal(localDst); err != nil || !ok {
		t.Errorf("StatLocal after download: ok=%v err=%v", ok, err)
	}

	// ── directories are refused, in both directions ─────────────────────────
	if _, err := sftppkg.Upload(r, filepath.Join(localDir, "sub"), path.Join(remoteDir, "sub")); err == nil {
		t.Error("uploading a missing path should fail")
	}
	if err := os.Mkdir(filepath.Join(localDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err = sftppkg.Upload(r, filepath.Join(localDir, "sub"), path.Join(remoteDir, "sub"))
	if !errors.Is(err, sftppkg.ErrIsDir) {
		t.Errorf("Upload of a directory: got %v, want ErrIsDir", err)
	}
	_, err = sftppkg.Download(r, path.Join(remoteDir, "adir"), filepath.Join(localDir, "adir"))
	if !errors.Is(err, sftppkg.ErrIsDir) {
		t.Errorf("Download of a directory: got %v, want ErrIsDir", err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// ── in-process SFTP server ──────────────────────────────────────────────────

type sftpServer struct {
	host    string
	port    int
	hostKey xssh.PublicKey
}

func (s *sftpServer) server() model.Server {
	return model.Server{
		Host: s.host, Port: s.port,
		User: "tester", Auth: model.AuthPassword, Password: "secret",
	}
}

func (s *sftpServer) trusted(t *testing.T) sshpkg.Options {
	t.Helper()
	p := filepath.Join(t.TempDir(), "known_hosts")
	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	line := knownhosts.Line([]string{knownhosts.Normalize(addr)}, s.hostKey)
	if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return sshpkg.Options{KnownHostsFiles: []string{p}}
}

func startSFTPServer(t *testing.T) *sftpServer {
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

	// Cleanups run last-in-first-out: close the listener, then drain the
	// handlers it spawned.
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
				serveSFTP(conn, cfg)
			}()
		}
	}()

	return &sftpServer{host: host, port: port, hostKey: signer.PublicKey()}
}

func serveSFTP(conn net.Conn, cfg *xssh.ServerConfig) {
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
		go func() {
			for req := range chReqs {
				// The client asks for the sftp subsystem and nothing else.
				ok := req.Type == "subsystem" && len(req.Payload) > 4 &&
					string(req.Payload[4:]) == "sftp"
				_ = req.Reply(ok, nil)
			}
		}()
		go func() {
			defer ch.Close()
			server, err := pkgsftp.NewServer(ch)
			if err != nil {
				return
			}
			defer server.Close()
			_ = server.Serve()
		}()
	}
}
