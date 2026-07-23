package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadKeysMissingIsNotAnError(t *testing.T) {
	s := New(t.TempDir())
	keys, err := s.LoadKeys()
	if err != nil {
		t.Fatalf("a missing keys.json must not be an error: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("got %v, want nothing", keys)
	}
}

func TestLoadKeysRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	body := `{"sidebar.delete": ["ctrl+d"], "tabs.close": []}`
	if err := os.WriteFile(filepath.Join(dir, "keys.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	keys, err := s.LoadKeys()
	if err != nil {
		t.Fatalf("LoadKeys: %v", err)
	}
	want := map[string][]string{"sidebar.delete": {"ctrl+d"}, "tabs.close": {}}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("got %v, want %v", keys, want)
	}
}

// TestBrokenKeysJSONIsAnError is the opposite of LoadUIState's contract, and
// deliberately so: ui.json is view sludge nobody wrote by hand, while keys.json
// is a statement about the keyboard. Swallowing a syntax error in it looks
// exactly like the rebinding not working.
func TestBrokenKeysJSONIsAnError(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := os.WriteFile(filepath.Join(dir, "keys.json"), []byte(`{"a": `), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadKeys(); err == nil {
		t.Fatal("a corrupt keys.json was accepted")
	}

	// ui.json in the same directory still fails soft, so the two files keep
	// their different promises.
	if err := os.WriteFile(filepath.Join(dir, "ui.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.LoadUIState(); got.SortRecent || len(got.Collapsed) != 0 {
		t.Errorf("a broken ui.json should give the zero value, got %+v", got)
	}
}
