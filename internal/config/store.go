// Package config owns everything under ${XDG_CONFIG_HOME:-~/.config}/ssh-client:
// the servers.json list and the keys/ directory. It knows nothing about the UI.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/pyjhoop/ssh-client/internal/model"
)

const (
	appDir          = "ssh-client"
	serversFile     = "servers.json"
	keysDir         = "keys"
	knownHostsFile  = "known_hosts"
	warnFile        = ".plaintext-warning-ack"
	userKnownHosts  = ".ssh/known_hosts"
	knownHostsPerms = 0o600
)

// Store is the on-disk server list.
type Store struct {
	dir string
}

// New returns a store rooted at dir. Used by tests; production code calls
// Default.
func New(dir string) *Store {
	return &Store{dir: dir}
}

// Default resolves the XDG config location.
func Default() (*Store, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return New(filepath.Join(base, appDir)), nil
}

// Dir is the root config directory.
func (s *Store) Dir() string { return s.dir }

// Path is the servers.json path.
func (s *Store) Path() string { return filepath.Join(s.dir, serversFile) }

// KeysDir is the directory private keys are written to.
func (s *Store) KeysDir() string { return filepath.Join(s.dir, keysDir) }

// KnownHostsPath is the only known_hosts file we ever write to. The user's
// OpenSSH file is read but never modified — it belongs to their other tools.
func (s *Store) KnownHostsPath() string { return filepath.Join(s.dir, knownHostsFile) }

// KnownHostsFiles lists the known_hosts files to verify against, most-specific
// last. Missing files are skipped: knownhosts.New fails on a path that does not
// exist, and a fresh install has neither.
func (s *Store) KnownHostsFiles() []string {
	var paths []string
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, filepath.FromSlash(userKnownHosts)))
	}
	paths = append(paths, s.KnownHostsPath())

	existing := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			existing = append(existing, p)
		}
	}
	return existing
}

// AppendKnownHost adds one known_hosts line to our own file, creating it 0600.
func (s *Store) AppendKnownHost(line string) error {
	if strings.TrimSpace(line) == "" {
		return errors.New("empty known_hosts line")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	path := s.KnownHostsPath()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, knownHostsPerms)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Load reads the server list. A missing file is an empty list, not an error.
func (s *Store) Load() ([]model.Server, error) {
	b, err := os.ReadFile(s.Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.Path(), err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, nil
	}
	var servers []model.Server
	if err := json.Unmarshal(b, &servers); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Path(), err)
	}
	return servers, nil
}

// Save writes the whole list back, atomically.
func (s *Store) Save(servers []model.Server) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	if servers == nil {
		servers = []model.Server{}
	}
	b, err := json.MarshalIndent(servers, "", "  ")
	if err != nil {
		return fmt.Errorf("encode servers: %w", err)
	}
	b = append(b, '\n')

	tmp := s.Path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.Path()); err != nil {
		return fmt.Errorf("replace %s: %w", s.Path(), err)
	}
	return nil
}

// Add appends a server, assigning an ID when it has none. The stored copy is
// returned so the caller sees the generated ID.
func (s *Store) Add(srv model.Server) (model.Server, error) {
	if srv.ID == "" {
		srv.ID = uuid.NewString()
	}
	if srv.Port == 0 {
		srv.Port = model.DefaultPort
	}
	servers, err := s.Load()
	if err != nil {
		return srv, err
	}
	servers = append(servers, srv)
	if err := s.Save(servers); err != nil {
		return srv, err
	}
	return srv, nil
}

// Update replaces the entry with the same ID, keeping its position in the list.
func (s *Store) Update(srv model.Server) error {
	if srv.ID == "" {
		return errors.New("cannot update a server without an ID")
	}
	if srv.Port == 0 {
		srv.Port = model.DefaultPort
	}
	servers, err := s.Load()
	if err != nil {
		return err
	}
	for i := range servers {
		if servers[i].ID == srv.ID {
			servers[i] = srv
			return s.Save(servers)
		}
	}
	return fmt.Errorf("no server with id %q", srv.ID)
}

// Remove deletes the entry with the given ID, along with the private key we
// generated for it. A key the user pointed us at (~/.ssh/id_ed25519) is theirs,
// not ours, so only paths inside KeysDir are deleted.
func (s *Store) Remove(id string) error {
	servers, err := s.Load()
	if err != nil {
		return err
	}
	var removed *model.Server
	kept := servers[:0]
	for _, srv := range servers {
		if srv.ID == id {
			removed = &srv
			continue
		}
		kept = append(kept, srv)
	}
	if err := s.Save(kept); err != nil {
		return err
	}
	if removed != nil && s.OwnsKey(removed.KeyPath) {
		if err := os.Remove(removed.KeyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", removed.KeyPath, err)
		}
	}
	return nil
}

// OwnsKey reports whether path is a file we wrote into KeysDir. Anything
// outside it — or reaching outside it via ".." — is not ours to delete.
func (s *Store) OwnsKey(path string) bool {
	if path == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	dir, err := filepath.Abs(s.KeysDir())
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(dir, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// SaveKey writes a pasted private key body to keys/<id>.pem with 0600
// permissions and returns the path.
func (s *Store) SaveKey(id, body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", errors.New("empty key body")
	}
	if id == "" {
		id = uuid.NewString()
	}
	if err := os.MkdirAll(s.KeysDir(), 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", s.KeysDir(), err)
	}
	path := filepath.Join(s.KeysDir(), id+".pem")

	// OpenSSH refuses keys without a trailing newline.
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	// WriteFile respects umask on creation, so force the mode explicitly.
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("chmod %s: %w", path, err)
	}
	return path, nil
}

// PlaintextWarningSeen reports whether the "passwords are stored in plaintext"
// warning has already been shown once.
func (s *Store) PlaintextWarningSeen() bool {
	_, err := os.Stat(filepath.Join(s.dir, warnFile))
	return err == nil
}

// MarkPlaintextWarningSeen records that the warning has been shown.
func (s *Store) MarkPlaintextWarningSeen() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	path := filepath.Join(s.dir, warnFile)
	if err := os.WriteFile(path, []byte("shown\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
