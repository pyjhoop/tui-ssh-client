// Package model holds the data structures shared by the ui, config and ssh
// packages. It imports nothing from the rest of the project.
package model

import (
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// AuthMethod is how we authenticate against a server.
type AuthMethod string

const (
	AuthPassword AuthMethod = "password"
	AuthKey      AuthMethod = "key"
)

// DefaultPort is used when the form leaves the port field empty.
const DefaultPort = 22

// Server is a saved connection entry.
type Server struct {
	ID       string     `json:"id"`
	Name     string     `json:"name,omitempty"`
	Host     string     `json:"host"`
	Port     int        `json:"port"`
	User     string     `json:"user"`
	Auth     AuthMethod `json:"auth"`
	Password string     `json:"password,omitempty"` // v0: stored in plaintext
	KeyPath  string     `json:"key_path,omitempty"`

	// Group is the one-level folder this server sits in; empty means ungrouped.
	// There is no group table — a group is just "the servers carrying this
	// string", so renaming one moves it and the last one leaving deletes it.
	Group string `json:"group,omitempty"`
	// LastUsed is set when a session opens successfully, for the recent-first
	// sort. omitempty keeps it out of files written before there was one.
	LastUsed time.Time `json:"last_used,omitempty"`
}

// FilterKey is the haystack the sidebar filter matches against: name, user,
// host and group in one lowercased string, so "prod db" style typing works.
func (s Server) FilterKey() string {
	return strings.ToLower(strings.Join([]string{
		s.Name, s.User, s.Host, s.Group,
	}, " "))
}

// Title is the label shown in the sidebar list.
func (s Server) Title() string {
	if n := strings.TrimSpace(s.Name); n != "" {
		return n
	}
	return s.UserHost()
}

// UserHost renders the canonical user@host form.
func (s Server) UserHost() string {
	return fmt.Sprintf("%s@%s", s.User, s.Host)
}

// Addr is the dial target, host:port.
func (s Server) Addr() string {
	port := s.Port
	if port == 0 {
		port = DefaultPort
	}
	return fmt.Sprintf("%s:%d", s.Host, port)
}

// Description is the secondary line shown in the sidebar list.
func (s Server) Description() string {
	return fmt.Sprintf("%s:%d · %s", s.Host, s.effectivePort(), s.Auth)
}

func (s Server) effectivePort() int {
	if s.Port == 0 {
		return DefaultPort
	}
	return s.Port
}

// Validate reports whether the entry is complete enough to be saved.
func (s Server) Validate() error {
	if strings.TrimSpace(s.Host) == "" {
		return errors.New("host is required")
	}
	if strings.TrimSpace(s.User) == "" {
		return errors.New("user is required")
	}
	if s.Port < 0 || s.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	switch s.Auth {
	case AuthPassword:
		if s.Password == "" {
			return errors.New("password is required")
		}
	case AuthKey:
		if strings.TrimSpace(s.KeyPath) == "" {
			return errors.New("key path (or pasted key body) is required")
		}
	default:
		return fmt.Errorf("unknown auth method %q", s.Auth)
	}
	return nil
}

// FileEntry is one row in a file pane. The local filesystem and a remote SFTP
// listing are both reduced to this so the two panes can be treated the same.
type FileEntry struct {
	Name    string
	Size    int64
	Mode    fs.FileMode
	ModTime time.Time
	IsDir   bool
}

// SortEntries orders a listing the way both panes must show it: directories
// first, then names, case-insensitively. Sorting lives here so local and remote
// can never drift apart.
func SortEntries(entries []FileEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		la, lb := strings.ToLower(a.Name), strings.ToLower(b.Name)
		if la != lb {
			return la < lb
		}
		return a.Name < b.Name
	})
}
