package sftp

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pyjhoop/ssh-client/internal/model"
)

// ErrIsDir rejects a directory handed to a transfer. Recursive copies are v3;
// until then this is a hard refusal rather than a silent skip.
var ErrIsDir = errors.New("directories are not supported yet")

// Upload copies a local file to the remote, giving the destination the source's
// permission bits. The destination is truncated, not renamed into place —
// resuming and cleaning up partial files is v3's problem.
func Upload(r *Remote, localPath, remotePath string) (int64, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", localPath, err)
	}
	if info.IsDir() {
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

	n, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return n, fmt.Errorf("upload %s: %w", remotePath, copyErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("upload %s: %w", remotePath, closeErr)
	}
	// Mode is best-effort: some servers refuse chmod, and a transferred file is
	// still a successful transfer.
	_ = r.sc.Chmod(remotePath, info.Mode().Perm())
	return n, nil
}

// Download is Upload's mirror image.
func Download(r *Remote, remotePath, localPath string) (int64, error) {
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

	n, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		return n, fmt.Errorf("download %s: %w", localPath, copyErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("download %s: %w", localPath, closeErr)
	}
	_ = os.Chmod(localPath, info.Mode().Perm())
	return n, nil
}

// StatLocal reports the entry at p and whether it exists. Like Remote.Stat, a
// missing file is the answer rather than an error — it drives the overwrite
// warning on the confirmation panel.
func StatLocal(p string) (model.FileEntry, bool, error) {
	info, err := os.Stat(p)
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
