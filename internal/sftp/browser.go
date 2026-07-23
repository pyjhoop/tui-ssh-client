// Package sftp is the file-transfer half of the client: directory listings and
// file copies, over SFTP for the remote side and plain os calls for the local
// one. It builds on internal/ssh's Dial so there is still exactly one place
// that opens a network connection, and it knows nothing about Bubble Tea.
package sftp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pyjhoop/tui-ssh-client/internal/model"
)

// Browser is one side of the split view. Local and Remote both implement it so
// the UI can treat the two panes symmetrically — including the path helpers,
// because a remote path is always slash-separated no matter what the local OS
// uses.
type Browser interface {
	List(dir string) ([]model.FileEntry, error)
	// Stat reports the entry at p and whether it exists at all; a missing path
	// is an answer, not an error. It does not follow symlinks, which is what
	// lets Plan walk a tree without chasing a cycle.
	Stat(p string) (model.FileEntry, bool, error)
	Home() (string, error)
	Join(dir, name string) string
	Parent(dir string) string
	Label() string

	// Remove and Rename are the file-management half. They live on the
	// interface so the UI never has to know which side of the split view it is
	// acting on.
	Remove(p string, recursive bool) error
	Rename(oldPath, newPath string) error
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

// Stat is StatLocal as a method, so Local satisfies Browser.
func (Local) Stat(p string) (model.FileEntry, bool, error) { return StatLocal(p) }

// Remove deletes a file, or a whole tree when recursive. A non-recursive call
// on a non-empty directory fails, which is what the confirmation panel relies
// on to make the user say yes to a recursive delete explicitly.
func (Local) Remove(p string, recursive bool) error {
	var err error
	if recursive {
		err = os.RemoveAll(p)
	} else {
		err = os.Remove(p)
	}
	if err != nil {
		return fmt.Errorf("remove %s: %w", p, err)
	}
	return nil
}

func (Local) Rename(oldPath, newPath string) error {
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename %s: %w", oldPath, err)
	}
	return nil
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
