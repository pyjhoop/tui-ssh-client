package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/model"
)

func groupedServers() []model.Server {
	return []model.Server{
		{ID: "1", Name: "laptop", Host: "10.0.0.1", User: "me", Port: 22},
		{ID: "2", Name: "web-1", Host: "10.0.1.1", User: "deploy", Port: 22, Group: "prod"},
		{ID: "3", Name: "web-2", Host: "10.0.1.2", User: "deploy", Port: 22, Group: "prod"},
		{ID: "4", Name: "prodigy", Host: "10.0.2.1", User: "root", Port: 22, Group: "staging"},
	}
}

// headerCount counts the group rows currently in the list.
func headerCount(s *sidebar) int {
	n := 0
	for _, li := range s.list.Items() {
		if it, ok := li.(item); ok && it.header {
			n++
		}
	}
	return n
}

// TestNoGroupsLooksLikeV4 pins the promise that upgrading changes nothing for
// someone who does not use groups: no headers, one row per server plus Connect.
func TestNoGroupsLooksLikeV4(t *testing.T) {
	servers := []model.Server{
		{ID: "1", Name: "a", Host: "a", User: "u"},
		{ID: "2", Name: "b", Host: "b", User: "u"},
	}
	s := newSidebar(servers, 28, 10)

	if got := headerCount(&s); got != 0 {
		t.Errorf("ungrouped list has %d headers, want 0", got)
	}
	if got := len(s.list.Items()); got != len(servers)+1 {
		t.Errorf("list has %d rows, want %d", got, len(servers)+1)
	}
}

func TestCollapsedGroupHidesItsServers(t *testing.T) {
	s := newSidebar(groupedServers(), 28, 20)

	// Connect + laptop + 2 headers + 3 grouped servers.
	full := len(s.list.Items())
	if full != 7 {
		t.Fatalf("expanded list has %d rows, want 7", full)
	}

	s.SetCollapsed([]string{"prod"})
	if got := len(s.list.Items()); got != full-2 {
		t.Errorf("collapsing prod left %d rows, want %d", got, full-2)
	}
	if got := headerCount(&s); got != 2 {
		t.Errorf("headers = %d, want both still there", got)
	}

	// A folded group with a live session inside must still say so.
	s.SetOpen(map[string]int{"2": 1})
	found := false
	for _, li := range s.list.Items() {
		it := li.(item)
		if it.header && it.group == "prod" {
			found = true
			if !strings.Contains(it.Title(), "●") {
				t.Errorf("collapsed prod header = %q, want a session marker", it.Title())
			}
		}
	}
	if !found {
		t.Fatal("prod header disappeared")
	}

	s.SetCollapsed(nil)
	if got := len(s.list.Items()); got != full {
		t.Errorf("expanding again left %d rows, want %d", got, full)
	}
}

func TestFilterMatchesNameUserHostGroup(t *testing.T) {
	s := newSidebar(groupedServers(), 28, 20)
	s.list.SetFilterText("prod")

	var names []string
	for _, li := range s.list.VisibleItems() {
		it := li.(item)
		if it.header {
			t.Errorf("filtered list contains group header %q", it.group)
			continue
		}
		if it.connect {
			continue
		}
		names = append(names, it.server.Name)
	}

	// web-1/web-2 match through their group, prodigy through its name — and the
	// order is the list's own, not a relevance ranking.
	want := []string{"web-1", "web-2", "prodigy"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("filtered names = %v, want %v", names, want)
	}
}

// TestFilterSwallowsShortcutKeys is the modal rule applied to the list: while a
// filter is being typed, d/n/q are text, not delete/new/quit.
func TestFilterSwallowsShortcutKeys(t *testing.T) {
	dir := t.TempDir()
	app := New(config.New(dir))
	app.servers = groupedServers()
	app.sidebar.SetServers(app.servers)
	app.resize(120, 40)

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !app.sidebar.Filtering() {
		t.Fatal("/ did not start the filter")
	}

	for _, r := range "dnq" {
		cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if app.quitting {
			t.Fatalf("%q quit the app while filtering", r)
		}
		if app.confirm != nil {
			t.Fatalf("%q opened the delete confirmation while filtering", r)
		}
		if len(app.tabs) != 0 {
			t.Fatalf("%q opened a session while filtering", r)
		}
		_ = cmd
	}
	if got := app.sidebar.list.FilterValue(); got != "dnq" {
		t.Errorf("filter text = %q, want %q", got, "dnq")
	}

	// esc clears the filter and the shortcuts come back.
	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if app.sidebar.Filtering() {
		t.Fatal("esc did not leave the filter")
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !app.quitting {
		t.Error("q after esc did not quit")
	}
}

// TestFilterAppliedRestoresShortcuts covers the other exit: enter keeps the
// results on screen but hands the keyboard back.
func TestFilterAppliedRestoresShortcuts(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.servers = groupedServers()
	app.sidebar.SetServers(app.servers)
	app.resize(120, 40)

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("web")})
	app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})

	if app.sidebar.list.FilterState() != list.FilterApplied {
		t.Fatalf("filter state = %v, want FilterApplied", app.sidebar.list.FilterState())
	}
	if app.sidebar.Filtering() {
		t.Fatal("an applied filter must not swallow keys")
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if !app.quitting {
		t.Error("q on an applied filter did not quit")
	}
}

// TestSidebarClickHitsVisibleRow is the one-row delegate's regression guard:
// the row you see is the row that gets selected, headers included.
func TestSidebarClickHitsVisibleRow(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.servers = groupedServers()
	app.sidebar.SetServers(app.servers)
	app.resize(120, 40)

	lines := strings.Split(app.View(), "\n")
	for wantIdx, label := range []string{"+ Connect", "laptop", "prod (2)", "web-1", "web-2", "staging (1)", "prodigy"} {
		row := -1
		for i, l := range lines {
			if strings.Contains(stripANSI(l), label) {
				row = i
				break
			}
		}
		if row < 0 {
			t.Fatalf("%q is not on screen", label)
		}
		got, ok := app.rowToIndex(row)
		if !ok || got != wantIdx {
			t.Errorf("%q renders on row %d; rowToIndex gave (%d, %v), want %d", label, row, got, ok, wantIdx)
		}
	}
}

// TestGroupHeaderIgnoresServerKeys: a header is not a server, so the keys that
// act on one must do nothing rather than act on whatever is nearby.
func TestGroupHeaderIgnoresServerKeys(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.servers = groupedServers()
	app.sidebar.SetServers(app.servers)
	app.resize(120, 40)

	app.sidebar.list.Select(2) // the prod header
	if it, _ := app.sidebar.Selected(); !it.header {
		t.Fatalf("row 2 is not the header: %+v", it)
	}

	for _, key := range []string{"n", "e", "d", "f"} {
		app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if app.confirm != nil || app.rightMode == rightForm || len(app.tabs) != 0 {
			t.Fatalf("%q acted on a group header (mode=%v confirm=%v tabs=%d)",
				key, app.rightMode, app.confirm != nil, len(app.tabs))
		}
	}

	// enter, on the other hand, folds it — and persists the choice.
	cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("folding produced no save command")
	}
	cmd()
	if got := app.store.LoadUIState().Collapsed; len(got) != 1 || got[0] != "prod" {
		t.Errorf("saved collapsed = %v, want [prod]", got)
	}
}

func TestSortRecentOrdersWithinGroups(t *testing.T) {
	servers := groupedServers()
	servers[1].LastUsed = time.Now().Add(-time.Hour) // web-1
	servers[2].LastUsed = time.Now()                 // web-2
	s := newSidebar(servers, 28, 20)

	names := func() []string {
		var out []string
		for _, li := range s.list.Items() {
			if it := li.(item); !it.header && !it.connect && it.server.Group == "prod" {
				out = append(out, it.server.Name)
			}
		}
		return out
	}

	if got := strings.Join(names(), ","); got != "web-1,web-2" {
		t.Errorf("file order = %q, want web-1,web-2", got)
	}
	s.SetSortRecent(true)
	if got := strings.Join(names(), ","); got != "web-2,web-1" {
		t.Errorf("recent order = %q, want web-2,web-1", got)
	}
}
