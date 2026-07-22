package sftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/pyjhoop/ssh-client/internal/model"
)

// ErrIsDir marks a directory handed to the single-file API. Recursive copies go
// through tree.go now, so this is an internal mistake rather than the refusal
// the user used to see.
var ErrIsDir = errors.New("not a regular file")

// ErrCancelled is what a transfer stopped by its context reports. The UI splits
// on errors.Is, never on the message.
var ErrCancelled = errors.New("transfer cancelled")

// copyChunk is the unit of work between two context checks. Small enough that
// cancelling feels immediate, large enough that the progress counter is not the
// bottleneck.
const copyChunk = 32 * 1024

// Progress is written by the transfer goroutine and read by the UI on a tick.
// Nothing else crosses that boundary — no channel, and no callback that could
// touch the model from the wrong goroutine.
//
// Every method is nil-safe: a caller with nothing to report passes nil.
type Progress struct {
	done  atomic.Int64
	total atomic.Int64
	name  atomic.Value // string: the file currently moving
}

func (p *Progress) Done() int64 {
	if p == nil {
		return 0
	}
	return p.done.Load()
}

func (p *Progress) Total() int64 {
	if p == nil {
		return 0
	}
	return p.total.Load()
}

func (p *Progress) Name() string {
	if p == nil {
		return ""
	}
	s, _ := p.name.Load().(string)
	return s
}

// SetTotal is called once, before the first byte moves: the bar can only be a
// percentage if the denominator is known up front (see Plan).
func (p *Progress) SetTotal(n int64) {
	if p != nil {
		p.total.Store(n)
	}
}

func (p *Progress) SetName(name string) {
	if p != nil {
		p.name.Store(name)
	}
}

func (p *Progress) add(n int64) {
	if p != nil && n > 0 {
		p.done.Add(n)
	}
}

// copyCtx is io.Copy with a cancellation point and a counter. Both directions
// use it, so upload and download cancel and report identically.
func copyCtx(ctx context.Context, dst io.Writer, src io.Reader, p *Progress) (int64, error) {
	buf := make([]byte, copyChunk)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, cancelled(err)
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			written, writeErr := dst.Write(buf[:n])
			total += int64(written)
			p.add(int64(written))
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

// cancelled maps a context error onto our sentinel so the UI never has to know
// that context is involved at all.
func cancelled(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrCancelled, err)
	}
	return err
}

// Upload copies one local file to the remote, giving the destination the
// source's permission bits.
//
// A failed or cancelled copy deletes the destination. A truncated file that
// looks complete is worse than no file at all, and the confirmation panel has
// already warned about overwriting whatever was there.
func Upload(ctx context.Context, r *Remote, localPath, remotePath string, p *Progress) (int64, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", localPath, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%s: %w", localPath, ErrIsDir)
	}

	src, err := os.Open(localPath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer src.Close()

	dst, err := r.sc.Create(remotePath)
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", remotePath, err)
	}

	p.SetName(info.Name())
	n, copyErr := copyCtx(ctx, dst, src, p)
	closeErr := dst.Close()
	if err := firstErr(copyErr, closeErr); err != nil {
		_ = r.sc.Remove(remotePath)
		return n, fmt.Errorf("upload %s: %w", remotePath, err)
	}
	// Mode is best-effort: some servers refuse chmod, and a transferred file is
	// still a successful transfer.
	_ = r.sc.Chmod(remotePath, info.Mode().Perm())
	return n, nil
}

// Download is Upload's mirror image, partial-file cleanup included.
func Download(ctx context.Context, r *Remote, remotePath, localPath string, p *Progress) (int64, error) {
	info, err := r.sc.Stat(remotePath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", remotePath, err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%s: %w", remotePath, ErrIsDir)
	}

	src, err := r.sc.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", remotePath, err)
	}
	defer src.Close()

	dst, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return 0, fmt.Errorf("create %s: %w", localPath, err)
	}

	p.SetName(info.Name())
	n, copyErr := copyCtx(ctx, dst, src, p)
	closeErr := dst.Close()
	if err := firstErr(copyErr, closeErr); err != nil {
		_ = os.Remove(localPath)
		return n, fmt.Errorf("download %s: %w", localPath, err)
	}
	_ = os.Chmod(localPath, info.Mode().Perm())
	return n, nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// StatLocal reports the entry at p and whether it exists. Like Remote.Stat, a
// missing file is the answer rather than an error — it drives the overwrite
// warning on the confirmation panel.
//
// It does not follow symlinks: Plan has to be able to tell a link from the
// thing it points at, or a cycle becomes an infinite walk.
func StatLocal(p string) (model.FileEntry, bool, error) {
	info, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return model.FileEntry{}, false, nil
		}
		return model.FileEntry{}, false, fmt.Errorf("stat %s: %w", p, err)
	}
	return model.FileEntry{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, true, nil
}
