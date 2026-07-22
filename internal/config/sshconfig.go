package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pyjhoop/ssh-client/internal/model"
)

// ImportGroup is the group import puts its entries in. It is only a default —
// nothing downstream treats these servers differently once they are saved.
const ImportGroup = "ssh_config"

// SSHConfigEntry is one importable Host block. Skip is set when we deliberately
// refuse it, and Reason says why — the preview shows both, because silently
// dropping half of someone's config is worse than explaining the gap.
type SSHConfigEntry struct {
	Alias    string
	Host     string
	User     string
	Port     int
	Identity string
	Skip     bool
	Reason   string
}

// Title is what the preview lists the entry under.
func (e SSHConfigEntry) Title() string { return e.Alias }

// Server converts an importable entry into a saved server. Auth is keyed off
// IdentityFile: without one we have no credential at all, and an empty password
// sends the first connection down the existing error path, which offers the
// form.
//
// The identity path is copied as-is, never into keys/. That file belongs to the
// user and to OpenSSH; a copy of ours would go stale the day they rotate it.
func (e SSHConfigEntry) Server() model.Server {
	srv := model.Server{
		Name:  e.Alias,
		Host:  e.Host,
		Port:  e.Port,
		User:  e.User,
		Group: ImportGroup,
	}
	if srv.Port == 0 {
		srv.Port = model.DefaultPort
	}
	if e.Identity != "" {
		srv.Auth = model.AuthKey
		srv.KeyPath = e.Identity
	} else {
		srv.Auth = model.AuthPassword
	}
	return srv
}

// DefaultSSHConfigPath is ~/.ssh/config, the only file import ever reads.
func DefaultSSHConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// ParseSSHConfig reads path and returns one entry per Host block, in file
// order. A missing file is not an error: it returns nil, nil.
//
// Only Host / HostName / User / Port / IdentityFile are understood. Include,
// Match and wildcard patterns are recorded as skipped rather than guessed at —
// pretending to support them would produce hosts that connect under OpenSSH and
// quietly fail here.
func ParseSSHConfig(path string) ([]SSHConfigEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	var (
		entries []SSHConfigEntry
		cur     *SSHConfigEntry
	)
	flush := func() {
		if cur != nil {
			entries = append(entries, *cur)
			cur = nil
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		key, value, ok := splitConfigLine(sc.Text())
		if !ok {
			continue
		}
		switch key {
		case "host":
			flush()
			cur = hostBlock(value)
		case "include":
			flush()
			entries = append(entries, SSHConfigEntry{
				Alias:  value,
				Skip:   true,
				Reason: "Include not supported",
			})
		case "match":
			flush()
			entries = append(entries, SSHConfigEntry{
				Alias:  value,
				Skip:   true,
				Reason: "Match not supported",
			})
		case "hostname":
			if cur != nil {
				cur.Host = value
			}
		case "user":
			if cur != nil {
				cur.User = value
			}
		case "port":
			if cur != nil {
				if p, err := strconv.Atoi(value); err == nil {
					cur.Port = p
				}
			}
		case "identityfile":
			if cur != nil {
				cur.Identity = expandTilde(value)
			}
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// HostName defaults to the alias, exactly as OpenSSH does. A block with
	// neither is not a host at all.
	for i := range entries {
		e := &entries[i]
		if e.Skip {
			continue
		}
		if e.Host == "" {
			e.Host = e.Alias
		}
		if e.Host == "" {
			e.Skip, e.Reason = true, "no HostName"
		}
	}
	return entries, nil
}

// hostBlock starts a new Host block, marking patterns we refuse to interpret.
func hostBlock(value string) *SSHConfigEntry {
	e := &SSHConfigEntry{Alias: value}
	if strings.ContainsAny(value, "*?!") || len(strings.Fields(value)) != 1 {
		e.Skip, e.Reason = true, "wildcard pattern"
	}
	return e
}

// splitConfigLine reduces one line to a lowercased keyword and its value,
// accepting both "Key value" and "Key=value". Comments and blank lines report
// false.
func splitConfigLine(line string) (key, value string, ok bool) {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", false
	}
	if i := strings.IndexByte(line, '='); i >= 0 && !strings.ContainsAny(line[:i], " \t") {
		key, value = line[:i], line[i+1:]
	} else if j := strings.IndexAny(line, " \t"); j >= 0 {
		key, value = line[:j], line[j+1:]
	} else {
		key, value = line, ""
	}
	key = strings.ToLower(strings.TrimSpace(key))
	value = strings.Trim(strings.TrimSpace(value), `"`)
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

// expandTilde resolves a leading ~ against the home directory. The file is not
// stat'ed: a path that does not exist is still recorded, and the existing error
// card explains the failure at connect time.
func expandTilde(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
