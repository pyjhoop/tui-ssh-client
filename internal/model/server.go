// Package model holds the data structures shared by the ui, config and ssh
// packages. It imports nothing from the rest of the project.
package model

import (
	"errors"
	"fmt"
	"strings"
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
