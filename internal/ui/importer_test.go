package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/model"
)

// openPreview drives the app into the import preview with a canned parse
// result, which is the same path the real parse command takes.
func openPreview(t *testing.T, app *App, entries []config.SSHConfigEntry) {
	t.Helper()
	app.resize(120, 40)
	app.prevRight = app.rightMode
	app.rightMode = rightImport
	app.focus = focusImport
	app.Update(sshConfigParsedMsg{path: "/tmp/ssh_config", entries: entries})
	if app.importing == nil {
		t.Fatal("preview did not open")
	}
}

func sampleEntries() []config.SSHConfigEntry {
	return []config.SSHConfigEntry{
		{Alias: "web-1", Host: "10.0.0.1", User: "deploy", Port: 22, Identity: "/home/me/.ssh/id_ed25519"},
		{Alias: "db-1", Host: "10.0.0.2", User: "postgres", Port: 22},
		{Alias: "*", Skip: true, Reason: "wildcard pattern"},
	}
}

func TestImportMarksDuplicates(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.servers = []model.Server{
		{ID: "1", Name: "already here", Host: "10.0.0.1", User: "deploy", Port: 22},
	}
	openPreview(t, app, sampleEntries())

	rows := app.importing.rows
	if !rows[0].dup {
		t.Error("web-1 duplicates a saved server but is not flagged")
	}
	if rows[0].marked {
		t.Error("a duplicate must start unchecked")
	}
	if !rows[1].marked {
		t.Error("a new entry must start checked")
	}
	if rows[2].marked || rows[2].importable() {
		t.Error("a skipped block must be neither checked nor selectable")
	}

	// The reason is on screen, not swallowed.
	if body := stripANSI(app.rightBody(80, 30)); !strings.Contains(body, "wildcard pattern") {
		t.Errorf("preview does not explain the skip:\n%s", body)
	}
}

// TestImportSavesOnce pins the batch write: one Save for the whole selection,
// not one per host.
func TestImportSavesOnce(t *testing.T) {
	dir := t.TempDir()
	app := New(config.New(dir))
	openPreview(t, app, sampleEntries())

	cmd := app.handleImportKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no import command")
	}
	msg := cmd()
	imported, ok := msg.(serversImportedMsg)
	if !ok {
		t.Fatalf("got %T, want serversImportedMsg", msg)
	}
	if imported.err != nil {
		t.Fatalf("import failed: %v", imported.err)
	}
	if imported.imported != 2 || imported.skipped != 1 {
		t.Errorf("imported %d skipped %d, want 2 and 1", imported.imported, imported.skipped)
	}

	app.Update(imported)
	if len(app.servers) != 2 {
		t.Fatalf("list has %d servers, want 2", len(app.servers))
	}
	if app.importing != nil || app.focus != focusSidebar {
		t.Errorf("preview stayed open after importing")
	}
	if !strings.Contains(app.status, "imported 2 servers (1 skipped)") {
		t.Errorf("status = %q", app.status)
	}

	// One write means the file on disk holds all of it after a single Save.
	saved, err := config.New(dir).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(saved) != 2 {
		t.Errorf("servers.json holds %d entries, want 2", len(saved))
	}
}

// TestImportKeepsIdentityPath: an IdentityFile is the user's own key. We record
// the path and copy nothing, so rotating that key does not leave us on a stale
// copy.
func TestImportKeepsIdentityPath(t *testing.T) {
	dir := t.TempDir()
	app := New(config.New(dir))
	openPreview(t, app, sampleEntries())

	msg := app.handleImportKey(tea.KeyMsg{Type: tea.KeyEnter})().(serversImportedMsg)
	app.Update(msg)

	var web model.Server
	for _, s := range app.servers {
		if s.Name == "web-1" {
			web = s
		}
	}
	if web.Auth != model.AuthKey {
		t.Fatalf("web-1 auth = %q, want key", web.Auth)
	}
	if web.KeyPath != "/home/me/.ssh/id_ed25519" {
		t.Errorf("KeyPath = %q, want the original path", web.KeyPath)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "keys")); err == nil && len(entries) > 0 {
		t.Errorf("import copied %d files into keys/, want none", len(entries))
	}
}

func TestImportSwallowsTabKeys(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.tabs = []*sessionTab{{name: "a"}, {name: "b"}}
	app.active = 0
	openPreview(t, app, sampleEntries())

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2"), Alt: true})
	if app.active != 0 {
		t.Errorf("alt+2 switched to tab %d while the preview was up", app.active)
	}
	if app.rightMode != rightImport {
		t.Errorf("right mode = %v, want the preview to still own the panel", app.rightMode)
	}
}

// TestImportCancelLeavesTabsAlone: esc goes back to whatever was on screen
// before, without touching the sessions.
func TestImportCancelLeavesTabsAlone(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.rightMode = rightTerminal
	app.tabs = []*sessionTab{{name: "a"}}
	app.active = 0
	openPreview(t, app, sampleEntries())

	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if app.importing != nil {
		t.Error("esc did not close the preview")
	}
	if app.rightMode != rightTerminal {
		t.Errorf("right mode = %v, want rightTerminal restored", app.rightMode)
	}
	if len(app.tabs) != 1 {
		t.Errorf("cancelling the import closed a session")
	}
}

func TestImportToggleAndSelectAll(t *testing.T) {
	app := New(config.New(t.TempDir()))
	openPreview(t, app, sampleEntries())
	im := app.importing

	im.toggleAll() // something was marked, so this clears
	for i, r := range im.rows {
		if r.marked {
			t.Errorf("row %d still marked after clearing", i)
		}
	}
	im.toggleAll() // nothing marked, so this selects every importable row
	if !im.rows[0].marked || !im.rows[1].marked || im.rows[2].marked {
		t.Errorf("select-all marked the wrong rows: %+v", im.rows)
	}

	// space on a skipped row does nothing at all.
	im.cursor = 2
	im.toggle()
	if im.rows[2].marked {
		t.Error("space selected a skipped block")
	}
}

func TestImportWithNoBlocksReturnsToList(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.resize(120, 40)
	app.rightMode = rightEmpty
	app.prevRight = rightEmpty
	app.rightMode = rightImport
	app.focus = focusImport

	app.Update(sshConfigParsedMsg{path: "/tmp/empty", entries: nil})
	if app.importing != nil || app.rightMode != rightEmpty || app.focus != focusSidebar {
		t.Errorf("an empty config left the preview up (mode=%v focus=%v)", app.rightMode, app.focus)
	}
	if !strings.Contains(app.status, "no Host blocks") {
		t.Errorf("status = %q", app.status)
	}
}
