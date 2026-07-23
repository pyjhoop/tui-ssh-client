package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pyjhoop/ssh-client/internal/model"
	"github.com/pyjhoop/ssh-client/internal/vault"
)

// bundleVersion is the shared format. It is checked on apply so a newer machine
// cannot quietly hand an older one a file it would misread.
const bundleVersion = 1

// localBackupFile is written just before a pull replaces the local list, so an
// unwanted pull is undoable without going back to the remote.
const localBackupFile = "ssh-client.local.bak"

// bundle is everything that defines "my servers": the list, the secrets and the
// host keys. It is a single JSON document rather than a tar because it has three
// members and never needs streaming.
//
// The whole thing is encrypted before it leaves this machine. Not one field —
// not even a host name — is meant to be readable by anyone holding the repo.
type bundle struct {
	Version    int            `json:"version"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Device     string         `json:"device,omitempty"`
	Servers    []model.Server `json:"servers"`
	Secrets    vault.Secrets  `json:"secrets"`
	KnownHosts string         `json:"known_hosts,omitempty"`
}

// ErrBundleVersion is a bundle written by a version that knows more than we do.
var ErrBundleVersion = errors.New("unsupported bundle version")

// ApplyReport is what the pull says it did. The conflicting host keys are the
// part that matters: they are the one thing we refuse to decide silently.
type ApplyReport struct {
	Servers        int      // entries the local list was replaced with
	KnownHostsKept int      // lines already on this machine
	KnownHostsNew  int      // lines the bundle added
	Conflicts      []string // hosts where the bundle disagreed and local won
	BackupPath     string   // where the previous local list was saved
}

// Bundle packs everything that defines "my servers" into one blob: the server
// list, the secrets and known_hosts. The caller encrypts it — nothing in this
// format is ever written or sent in the clear.
func (s *Store) Bundle(sec vault.Secrets) ([]byte, error) {
	servers, err := s.Load()
	if err != nil {
		return nil, err
	}
	if servers == nil {
		servers = []model.Server{}
	}

	known, err := os.ReadFile(s.KnownHostsPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", s.KnownHostsPath(), err)
	}

	if sec.Version == 0 {
		sec.Version = vault.CurrentVersion
	}
	b, err := json.Marshal(bundle{
		Version:    bundleVersion,
		UpdatedAt:  time.Now().UTC(),
		Device:     deviceName(),
		Servers:    servers,
		Secrets:    sec,
		KnownHosts: string(known),
	})
	if err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	return b, nil
}

// BundleServers reports how many servers a decrypted bundle holds, for the
// confirmation shown before a pull replaces anything.
func BundleServers(b []byte) (int, time.Time, string, error) {
	var bun bundle
	if err := json.Unmarshal(b, &bun); err != nil {
		return 0, time.Time{}, "", fmt.Errorf("parse bundle: %w", err)
	}
	if bun.Version > bundleVersion {
		return 0, time.Time{}, "", fmt.Errorf("%w: %d", ErrBundleVersion, bun.Version)
	}
	return len(bun.Servers), bun.UpdatedAt, bun.Device, nil
}

// ApplyBundle replaces the local list and secrets with the bundle's, and merges
// known_hosts as a union.
//
// known_hosts is the one thing never replaced wholesale. Overwriting it would
// drop the hosts approved only on this machine, and the next connection to one
// of them would show a trust-on-first-use prompt again — training the user to
// wave fingerprints through is a security regression, not a sync detail. A host
// that carries a *different* key on each side keeps the local one and is
// reported: that disagreement is exactly what a machine-in-the-middle looks
// like, so it must not be resolved quietly.
func (s *Store) ApplyBundle(b []byte, sec *vault.Secrets) (ApplyReport, error) {
	var bun bundle
	if err := json.Unmarshal(b, &bun); err != nil {
		return ApplyReport{}, fmt.Errorf("parse bundle: %w", err)
	}
	if bun.Version > bundleVersion {
		return ApplyReport{}, fmt.Errorf("%w: %d", ErrBundleVersion, bun.Version)
	}

	var rep ApplyReport

	// Back up what is about to be replaced before replacing it. A pull the user
	// did not mean is otherwise unrecoverable.
	if old, err := os.ReadFile(s.Path()); err == nil {
		path := s.backupPath(localBackupFile)
		if err := os.WriteFile(path, old, 0o600); err != nil {
			return rep, fmt.Errorf("write %s: %w", path, err)
		}
		rep.BackupPath = path
	} else if !errors.Is(err, os.ErrNotExist) {
		return rep, fmt.Errorf("read %s: %w", s.Path(), err)
	}

	if err := s.Save(bun.Servers); err != nil {
		return rep, err
	}
	rep.Servers = len(bun.Servers)

	if sec != nil {
		*sec = bun.Secrets
		if sec.Version == 0 {
			sec.Version = vault.CurrentVersion
		}
	}

	merged, kept, added, conflicts, err := s.mergeKnownHosts(bun.KnownHosts)
	if err != nil {
		return rep, err
	}
	rep.KnownHostsKept, rep.KnownHostsNew, rep.Conflicts = kept, added, conflicts
	if added > 0 {
		if err := s.writeKnownHosts(merged); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// mergeKnownHosts unions the incoming lines into ours, returning the merged file
// plus what happened. Local always wins a disagreement.
func (s *Store) mergeKnownHosts(incoming string) (merged string, kept, added int, conflicts []string, err error) {
	local, err := os.ReadFile(s.KnownHostsPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", 0, 0, nil, fmt.Errorf("read %s: %w", s.KnownHostsPath(), err)
	}

	localLines := knownHostLines(string(local))
	// Index every (host pattern, key type) we already trust, so an incoming line
	// can be told apart from a *different* key for a host we know.
	have := make(map[string]string, len(localLines))
	seen := make(map[string]bool, len(localLines))
	for _, l := range localLines {
		seen[l.raw] = true
		for _, k := range l.keys() {
			have[k] = l.key
		}
		kept++
	}

	out := append([]string(nil), rawLines(localLines)...)
	clash := map[string]bool{}
	for _, l := range knownHostLines(incoming) {
		if seen[l.raw] {
			continue
		}
		conflicting := false
		for _, k := range l.keys() {
			if existing, ok := have[k]; ok && existing != l.key {
				conflicting = true
				if !clash[l.hosts] {
					clash[l.hosts] = true
					conflicts = append(conflicts, l.hosts)
				}
			}
		}
		if conflicting {
			continue
		}
		seen[l.raw] = true
		for _, k := range l.keys() {
			have[k] = l.key
		}
		out = append(out, l.raw)
		added++
	}

	sort.Strings(conflicts)
	if len(out) == 0 {
		return "", kept, added, conflicts, nil
	}
	return strings.Join(out, "\n") + "\n", kept, added, conflicts, nil
}

func (s *Store) writeKnownHosts(content string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	path := s.KnownHostsPath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), knownHostsPerms); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, knownHostsPerms); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// hostLine is one parsed known_hosts entry. Only the three fields that decide
// identity are pulled out — comments and markers ride along in raw.
type hostLine struct {
	raw     string
	hosts   string // the comma-separated pattern list, as written
	keyType string
	key     string
}

// keys is the identity of this line, one entry per host pattern: two lines
// clash when they name the same host with the same algorithm and a different
// key.
func (l hostLine) keys() []string {
	patterns := strings.Split(l.hosts, ",")
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p+" "+l.keyType)
		}
	}
	return out
}

func knownHostLines(s string) []hostLine {
	var out []hostLine
	for _, raw := range strings.Split(s, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		// A leading @cert-authority or @revoked marker shifts everything along.
		if strings.HasPrefix(fields[0], "@") {
			fields = fields[1:]
		}
		if len(fields) < 3 {
			continue
		}
		out = append(out, hostLine{raw: line, hosts: fields[0], keyType: fields[1], key: fields[2]})
	}
	return out
}

func rawLines(lines []hostLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.raw
	}
	return out
}

func (s *Store) backupPath(name string) string {
	return filepath.Join(s.dir, name)
}

// deviceName labels a bundle with where it came from. It is a convenience in
// the pull confirmation, never an identity — nothing branches on it.
func deviceName() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return ""
}
