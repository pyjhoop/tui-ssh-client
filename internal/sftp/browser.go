// Package sftp is the file-transfer half of the client: directory listings and
// file copies, over SFTP for the remote side and plain os calls for the local
// one. It builds on internal/ssh's Dial so there is still exactly one place
// that opens a network connection, and it knows nothing about Bubble Tea.
package sftp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pyjhoop/ssh-client/internal/model"
)

// Browser is one side of the split view. Local and Remote both implement it so
// the UI can treat the two panes symmetrically — including the path helpers,
// because a remote path is always slash-separated no matter what the local OS
// uses.
type Browser interface {
	List(dir string) ([]model.FileEntry, error)
	Home() (string, error)
	Join(dir, name string) string
	Parent(dir string) string
	Label() string
}

// Local browses the machine the client runs on.
type Local struct{}

func (Local) Label() string { return "Local" }

func (Local) Join(dir, name string) string { return filepath.Join(dir, name) }

func (Local) Parent(dir string) string { return filepath.Dir(dir) }

func (Local) Home() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		wd, wdErr := os.Getwd()
		if wdErr != nil {
			return "/", nil
		}
		return wd, nil
	}
	return home, nil
}

// List reads a directory. Entries whose metadata cannot be read are still
// listed, with zero size: one unreadable file must not blank out the whole
// listing. "." is never included and ".." is drawn by the UI, not by us.
func (Local) List(dir string) ([]model.FileEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}

	entries := make([]model.FileEntry, 0, len(des))
	for _, de := range des {
		e := model.FileEntry{Name: de.Name(), IsDir: de.IsDir()}
		if info, err := de.Info(); err == nil {
			e.Size = info.Size()
			e.Mode = info.Mode()
			e.ModTime = info.ModTime()
			// A symlink to a directory should behave like a directory.
			if !e.IsDir && info.Mode()&os.ModeSymlink != 0 {
				if target, err := os.Stat(filepath.Join(dir, de.Name())); err == nil {
					e.IsDir = target.IsDir()
				}
			}
		}
		entries = append(entries, e)
	}
	model.SortEntries(entries)
	return entries, nil
}
