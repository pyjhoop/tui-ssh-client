package sftp

import (
	"errors"
	"fmt"
	"os"
	"path"

	pkgsftp "github.com/pkg/sftp"
	xssh "golang.org/x/crypto/ssh"

	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// Remote browses a server over SFTP. Its TCP connection is its own: closing the
// terminal session leaves the file panes alive, and vice versa.
type Remote struct {
	client *xssh.Client
	sc     *pkgsftp.Client
	label  string
}

// Connect dials the server and opens the SFTP subsystem. It blocks — including
// on the host key prompt — so callers must run it off the UI goroutine.
func Connect(srv model.Server, opts sshpkg.Options) (*Remote, error) {
	client, err := sshpkg.Dial(srv, opts)
	if err != nil {
		return nil, err
	}
	sc, err := pkgsftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: start sftp subsystem: %w", sshpkg.ErrSFTP, err)
	}
	return &Remote{client: client, sc: sc, label: srv.UserHost()}, nil
}

func (r *Remote) Label() string { return r.label }

func (r *Remote) Join(dir, name string) string { return path.Join(dir, name) }

func (r *Remote) Parent(dir string) string { return path.Dir(dir) }

// Home is the directory the pane opens on. Servers that do not answer Getwd
// still have a root.
func (r *Remote) Home() (string, error) {
	wd, err := r.sc.Getwd()
	if err != nil || wd == "" {
		return "/", nil
	}
	return wd, nil
}

func (r *Remote) List(dir string) ([]model.FileEntry, error) {
	infos, err := r.sc.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	entries := make([]model.FileEntry, 0, len(infos))
	for _, info := range infos {
		e := model.FileEntry{
			Name:    info.Name(),
			Size:    info.Size(),
			Mode:    info.Mode(),
			ModTime: info.ModTime(),
			IsDir:   info.IsDir(),
		}
		if !e.IsDir && info.Mode()&os.ModeSymlink != 0 {
			if target, err := r.sc.Stat(path.Join(dir, info.Name())); err == nil {
				e.IsDir = target.IsDir()
			}
		}
		entries = append(entries, e)
	}
	model.SortEntries(entries)
	return entries, nil
}

// Stat reports the entry at p and whether it exists at all. A missing file is
// not an error here: the caller is asking precisely to find that out.
//
// Like StatLocal it does not follow symlinks — Plan has to see the link itself
// to skip it rather than walk into a cycle.
func (r *Remote) Stat(p string) (model.FileEntry, bool, error) {
	info, err := r.sc.Lstat(p)
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

// Remove deletes a remote file, or a whole tree when recursive. pkg/sftp has no
// RemoveAll, so the tree walk is ours: children first, then the directory, or
// the server refuses to unlink a non-empty one.
func (r *Remote) Remove(p string, recursive bool) error {
	if !recursive {
		if err := r.sc.Remove(p); err != nil {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		return nil
	}
	if err := r.removeTree(p); err != nil {
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}

func (r *Remote) removeTree(p string) error {
	info, err := r.sc.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	// A symlink is unlinked, never descended into: following it would delete
	// whatever it happens to point at.
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		children, err := r.sc.ReadDir(p)
		if err != nil {
			return err
		}
		for _, c := range children {
			if err := r.removeTree(path.Join(p, c.Name())); err != nil {
				return err
			}
		}
	}
	return r.sc.Remove(p)
}

func (r *Remote) Rename(oldPath, newPath string) error {
	if err := r.sc.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename %s: %w", oldPath, err)
	}
	return nil
}

// Close tears down the subsystem and then the connection under it.
func (r *Remote) Close() error {
	if r == nil {
		return nil
	}
	scErr := r.sc.Close()
	clientErr := r.client.Close()
	if scErr != nil {
		return fmt.Errorf("close sftp: %w", scErr)
	}
	if clientErr != nil {
		return fmt.Errorf("close client: %w", clientErr)
	}
	return nil
}
