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
func (r *Remote) Stat(p string) (model.FileEntry, bool, error) {
	info, err := r.sc.Stat(p)
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
