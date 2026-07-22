package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const uiStateFile = "ui.json"

// UIState is view-only sludge: it must never hold anything the user would miss
// if the file were deleted. That is exactly why it is not part of servers.json —
// that file is data the user may edit by hand, this one is a UI leftover.
type UIState struct {
	// Collapsed lists the group names that are folded shut.
	Collapsed []string `json:"collapsed"`
	// SortRecent orders each group by last connection instead of file order.
	SortRecent bool `json:"sort_recent"`
}

// UIStatePath is where the view state is kept, next to servers.json.
func (s *Store) UIStatePath() string { return filepath.Join(s.dir, uiStateFile) }

// LoadUIState reads the saved view state. A missing or corrupt file is not an
// error: it yields the zero value, which is "everything expanded, file order".
// The app has to run fine with this file deleted.
func (s *Store) LoadUIState() UIState {
	var st UIState
	b, err := os.ReadFile(s.UIStatePath())
	if err != nil {
		return UIState{}
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return UIState{}
	}
	return st
}

// SaveUIState writes the view state atomically, the same way Save does.
func (s *Store) SaveUIState(st UIState) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.dir, err)
	}
	if st.Collapsed == nil {
		st.Collapsed = []string{}
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ui state: %w", err)
	}
	b = append(b, '\n')

	tmp := s.UIStatePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.UIStatePath()); err != nil {
		return fmt.Errorf("replace %s: %w", s.UIStatePath(), err)
	}
	return nil
}
