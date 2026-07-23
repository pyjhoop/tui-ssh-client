package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const keymapFile = "keys.json"

// KeysPath is where the user's key bindings live, next to servers.json.
func (s *Store) KeysPath() string { return filepath.Join(s.dir, keymapFile) }

// LoadKeys reads keys.json into a map of action id to keys. A missing file is
// not an error — most people never write one.
//
// A *broken* file is, and that is the whole difference from LoadUIState: ui.json
// is view sludge nobody wrote on purpose, while this file is a deliberate
// statement about how the keyboard should behave. Swallowing a syntax error in
// it is indistinguishable from the rebinding silently not working, which is the
// worst way for it to fail.
func (s *Store) LoadKeys() (map[string][]string, error) {
	b, err := os.ReadFile(s.KeysPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", s.KeysPath(), err)
	}

	var keys map[string][]string
	if err := json.Unmarshal(b, &keys); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.KeysPath(), err)
	}
	return keys, nil
}
