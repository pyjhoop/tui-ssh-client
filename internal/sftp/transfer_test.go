package sftp_test

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"testing"

	sftppkg "github.com/pyjhoop/tui-ssh-client/internal/sftp"
)

// TestProgressCountsAndCancels covers the two things the status line and the
// cancel key depend on: the counter ends at exactly the file size, and a
// cancelled transfer leaves nothing behind on either side.
func TestProgressCountsAndCancels(t *testing.T) {
	srv := startSFTPServer(t)
	r, err := sftppkg.Connect(srv.server(), srv.trusted(t))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer r.Close()

	localDir := t.TempDir()
	remoteDir := t.TempDir()

	// Several chunks' worth, so a cancellation has somewhere to land.
	body := make([]byte, 512*1024)
	for i := range body {
		body[i] = byte(i)
	}
	src := filepath.Join(localDir, "big.bin")
	if err := os.WriteFile(src, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// ── a complete upload counts every byte ─────────────────────────────────
	dst := path.Join(remoteDir, "big.bin")
	prog := &sftppkg.Progress{}
	prog.SetTotal(int64(len(body)))
	n, err := sftppkg.Upload(context.Background(), r, src, dst, prog)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if n != int64(len(body)) || prog.Done() != int64(len(body)) {
		t.Errorf("copied %d, counter %d, want %d", n, prog.Done(), len(body))
	}
	if prog.Name() != "big.bin" {
		t.Errorf("Name = %q, want big.bin", prog.Name())
	}

	// ── a cancelled upload removes the partial file ─────────────────────────
	cancelDst := path.Join(remoteDir, "cancelled.bin")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before the first chunk: the loop must notice immediately
	_, err = sftppkg.Upload(ctx, r, src, cancelDst, nil)
	if !errors.Is(err, sftppkg.ErrCancelled) {
		t.Fatalf("cancelled Upload: got %v, want ErrCancelled", err)
	}
	if _, err := os.Stat(filepath.Join(remoteDir, "cancelled.bin")); !os.IsNotExist(err) {
		t.Errorf("a cancelled upload left a partial file behind: %v", err)
	}

	// ── and so does a cancelled download ────────────────────────────────────
	localDst := filepath.Join(localDir, "cancelled.bin")
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_, err = sftppkg.Download(ctx2, r, dst, localDst, nil)
	if !errors.Is(err, sftppkg.ErrCancelled) {
		t.Fatalf("cancelled Download: got %v, want ErrCancelled", err)
	}
	if _, err := os.Stat(localDst); !os.IsNotExist(err) {
		t.Errorf("a cancelled download left a partial file behind: %v", err)
	}
}

// TestProgressIsNilSafe: a transfer with nothing to report passes nil, and the
// counters still answer.
func TestProgressIsNilSafe(t *testing.T) {
	var p *sftppkg.Progress
	if p.Done() != 0 || p.Total() != 0 || p.Name() != "" {
		t.Error("a nil Progress should read as empty")
	}
	p.SetTotal(10)
	p.SetName("x")
}
