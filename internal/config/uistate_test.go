package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pyjhoop/ssh-client/internal/model"
)

func TestUIStateRoundTrip(t *testing.T) {
	s := New(t.TempDir())

	want := UIState{Collapsed: []string{"prod", "staging"}, SortRecent: true}
	if err := s.SaveUIState(want); err != nil {
		t.Fatalf("SaveUIState: %v", err)
	}
	if got := s.LoadUIState(); !reflect.DeepEqual(got, want) {
		t.Errorf("LoadUIState = %+v, want %+v", got, want)
	}
}

func TestMissingUIStateIsZero(t *testing.T) {
	s := New(t.TempDir())
	if got := s.LoadUIState(); len(got.Collapsed) != 0 || got.SortRecent {
		t.Errorf("LoadUIState with no file = %+v, want zero", got)
	}
}

// TestCorruptUIStateIsIgnored pins the rule that ui.json is disposable: a
// half-written file must degrade to "everything expanded", never to an error
// the user has to deal with.
func TestCorruptUIStateIsIgnored(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if err := os.WriteFile(filepath.Join(dir, uiStateFile), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := s.LoadUIState(); len(got.Collapsed) != 0 || got.SortRecent {
		t.Errorf("LoadUIState on corrupt file = %+v, want zero", got)
	}
}

// TestLoadsV4ServersFile checks that adding group/last_used needs no migration:
// a file written before they existed still loads, ungrouped.
func TestLoadsV4ServersFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	body := `[{"id":"1","name":"web","host":"10.0.0.1","port":22,"user":"deploy","auth":"password","password":"x"}]`
	if err := os.WriteFile(filepath.Join(dir, serversFile), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	servers, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}
	if servers[0].Group != "" || !servers[0].LastUsed.IsZero() {
		t.Errorf("v4 entry = %+v, want empty group and zero LastUsed", servers[0])
	}
	if servers[0].Auth != model.AuthPassword {
		t.Errorf("auth = %q", servers[0].Auth)
	}
}

func TestAddAllWritesOnceAndAssignsIDs(t *testing.T) {
	s := New(t.TempDir())
	if _, err := s.Add(model.Server{Name: "existing", Host: "h", User: "u", Auth: model.AuthPassword, Password: "p"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	batch := []model.Server{
		{Name: "a", Host: "a", User: "u", Auth: model.AuthPassword},
		{Name: "b", Host: "b", User: "u", Auth: model.AuthPassword},
	}
	got, err := s.AddAll(batch)
	if err != nil {
		t.Fatalf("AddAll: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d servers, want 3", len(got))
	}
	for _, srv := range got {
		if srv.ID == "" {
			t.Errorf("%q has no ID", srv.Name)
		}
		if srv.Port != model.DefaultPort {
			t.Errorf("%q port = %d, want the default", srv.Name, srv.Port)
		}
	}
	// The batch is not mutated: the caller's slice is theirs.
	if batch[0].ID != "" {
		t.Errorf("AddAll assigned an ID into the caller's slice")
	}
}
