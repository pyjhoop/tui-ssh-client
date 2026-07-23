package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/tui-ssh-client/internal/ssh"
)

// helpApp is a root model with a couple of servers, big enough for the card.
func helpApp(t *testing.T) *App {
	t.Helper()
	app := New(config.New(t.TempDir()))
	app.servers = []model.Server{
		{ID: "1", Name: "alpha", Host: "a", User: "u", Port: 22},
		{ID: "2", Name: "beta", Host: "b", User: "u", Port: 22, Group: "prod"},
	}
	app.sidebar.SetServers(app.servers)
	app.resize(120, 40)
	return app
}

func pressRune(app *App, r rune) { app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) }

// TestHelpMatchesRealBindings is the promise the card is built on: every key it
// shows resolves, in the section it is shown under, to the action it claims.
// A help screen with its own table of strings is one that goes out of date.
func TestHelpMatchesRealBindings(t *testing.T) {
	app := helpApp(t)
	pressRune(app, '?')
	if app.help == nil {
		t.Fatal("? did not open the card")
	}

	for _, ctx := range contextOrder {
		for _, b := range app.keys.Bindings(ctx) {
			if b.Doc {
				continue // described here, dispatched by its own component
			}
			for _, key := range b.Keys {
				if got := app.keys.Action(ctx, key); got != b.Action {
					t.Errorf("the card lists %q under %s as %s, but it resolves to %q",
						key, ctx, b.Action, got)
				}
			}
		}
	}

	// And the rendered card really does name them.
	view := stripANSI(app.helpView(78, 30))
	if !strings.Contains(view, "Keyboard shortcuts") {
		t.Error("no title on the card")
	}
}

func TestHelpOpensOnCurrentContextFirst(t *testing.T) {
	app := sftpApp(t, 120, 40)
	pressRune(app, '?')
	if app.help == nil {
		t.Fatal("? did not open the card in the file panes")
	}
	if app.help.ctx != ctxSFTP {
		t.Fatalf("card opened on %q, want the sftp section", app.help.ctx)
	}
	body := stripANSI(app.helpView(78, 40))
	sftpAt := strings.Index(body, contextTitles[ctxSFTP])
	sidebarAt := strings.Index(body, contextTitles[ctxSidebar])
	if sftpAt < 0 || sidebarAt < 0 || sftpAt > sidebarAt {
		t.Errorf("the sftp section is not first:\n%s", body)
	}
}

// TestHelpSwallowsKeys: while the card is up nothing behind it may move — not
// the list, not the tabs, and above all not the shell.
func TestHelpSwallowsKeys(t *testing.T) {
	app := helpApp(t)
	app.sidebar.list.Select(1)
	before := app.sidebar.list.Index()

	pressRune(app, '?')
	pressRune(app, 'd') // would ask to delete
	if app.confirm != nil {
		t.Fatal("d reached the sidebar through the card")
	}
	if app.help != nil {
		t.Fatal("d should have closed the card")
	}
	if app.sidebar.list.Index() != before {
		t.Error("the cursor moved behind the card")
	}
	if app.quitting {
		t.Fatal("something quit")
	}

	// q closes it too, rather than quitting.
	pressRune(app, '?')
	pressRune(app, 'q')
	if app.help != nil || app.quitting {
		t.Fatalf("q did not just close the card (help=%v quitting=%v)", app.help != nil, app.quitting)
	}
}

// TestNoHelpInSession: inside a live session every key belongs to the remote
// shell, ? included. The way to the card is ctrl+b first.
func TestNoHelpInSession(t *testing.T) {
	app := helpApp(t)
	app.focus = focusSession
	app.rightMode = rightTerminal

	pressRune(app, '?')
	if app.help != nil {
		t.Fatal("? opened the card inside a session")
	}
	if app.helpAvailable() {
		t.Fatal("helpAvailable must be false in a session")
	}

	// The status line says how to get there instead.
	app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if app.focus != focusSidebar {
		t.Fatal("ctrl+b did not leave the session")
	}
	pressRune(app, '?')
	if app.help == nil {
		t.Fatal("? does not work after leaving the session")
	}
}

func TestHelpDoesNotStackOnModals(t *testing.T) {
	app := helpApp(t)
	app.confirm = &confirm{title: "Delete", accept: "[enter] delete"}
	pressRune(app, '?')
	if app.help != nil {
		t.Fatal("the card opened on top of a confirmation")
	}
	if app.confirm == nil {
		t.Fatal("the confirmation was dismissed by ?")
	}

	app.confirm = nil
	app.unlock = newUnlock(unlockOpen, "")
	pressRune(app, '?')
	if app.help != nil {
		t.Fatal("the card opened on top of the vault gate")
	}
}

// TestHelpRestoresState: the card changes nothing, so closing it leaves the
// screen exactly as it was.
func TestHelpRestoresState(t *testing.T) {
	app := sftpApp(t, 120, 40)
	app.focus = focusRemote
	app.remotePane.moveCursor(2)
	cursor := app.remotePane.cursor
	mode, focus := app.rightMode, app.focus

	pressRune(app, '?')
	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})

	if app.help != nil {
		t.Fatal("esc did not close the card")
	}
	if app.rightMode != mode || app.focus != focus || app.remotePane.cursor != cursor {
		t.Errorf("state changed: mode %v→%v focus %v→%v cursor %d→%d",
			mode, app.rightMode, focus, app.focus, cursor, app.remotePane.cursor)
	}
}

func TestHelpFilterIsSubstring(t *testing.T) {
	app := helpApp(t)
	pressRune(app, '?')
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !app.help.searching {
		t.Fatal("/ did not start a search")
	}
	for _, r := range "delete" {
		app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	body := stripANSI(app.helpView(78, 40))
	if !strings.Contains(body, "sidebar.delete") && !strings.Contains(body, "delete the highlighted server") {
		t.Errorf("the search did not find the sidebar delete:\n%s", body)
	}
	if !strings.Contains(body, "Files (SFTP)") {
		t.Errorf("the search is not flat across contexts:\n%s", body)
	}
	// Typing into the search may not act on the app behind it.
	if app.confirm != nil {
		t.Fatal("a letter typed into the search reached the sidebar")
	}
}

// TestHelpOverlayKeepsLayout: the card floats, in every mode, and the frame
// stays one exact rectangle with the vertical budget untouched.
func TestHelpOverlayKeepsLayout(t *testing.T) {
	sizes := [][2]int{{120, 40}, {100, 24}, {80, 30}}
	for _, size := range sizes {
		width, height := size[0], size[1]

		modes := map[string]*App{}
		plain := helpApp(t)
		plain.resize(width, height)
		modes["empty"] = plain

		files := sftpApp(t, width, height)
		modes["sftp"] = files

		formApp := helpApp(t)
		formApp.resize(width, height)
		formApp.rightMode, formApp.focus = rightForm, focusForm
		modes["form"] = formApp

		errApp := helpApp(t)
		errApp.resize(width, height)
		errApp.rightMode = rightError
		errApp.failErr = sshpkg.ErrAuth
		errApp.lastAttempt = errApp.servers[0]
		modes["error"] = errApp

		for name, app := range modes {
			app.openHelp()
			view := app.View()
			lines := strings.Split(view, "\n")
			if len(lines) != height {
				t.Fatalf("%s %dx%d: %d rows, want %d", name, width, height, len(lines), height)
			}
			for i, l := range lines {
				if got := ansi.StringWidth(l); got != width {
					t.Fatalf("%s %dx%d: row %d is %d wide, want %d", name, width, height, i, got, width)
				}
			}
		}

		// The three panes are still standing behind the card.
		if got := strings.Count(files.View(), "╭"); got < 3 {
			t.Errorf("%dx%d: the card replaced the panes (%d corners)", width, height, got)
		}
	}
}

// TestHelpWithColour repeats the layout check with colours on. Under `go test`
// stdout is not a terminal, so lipgloss strips every escape and the runs above
// never splice a styled row — which is the only case the width arithmetic can
// get wrong.
func TestHelpWithColour(t *testing.T) {
	before := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(before) })

	app := sftpApp(t, 100, 30)
	app.openHelp()

	view := app.View()
	if !strings.Contains(view, "\x1b[") {
		t.Fatal("the view carries no styling — this test is not testing anything")
	}
	for i, l := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(l); w != 100 {
			t.Errorf("styled row %d is %d columns wide, want 100", i, w)
		}
	}
	if got := strings.Count(stripANSI(strings.Split(view, "\n")[topMargin]), "╭"); got != 3 {
		t.Errorf("%d panel tops, want 3 — the card must float, not replace", got)
	}
}

// ── the status line ─────────────────────────────────────────────────────────

// TestHelpCellSurvivesWarnings is the bug this version set out to fix: a yellow
// warning, an error, a transfer or a drag used to take the whole status line
// and the shortcut hints went with it — including the one key that shows the
// rest.
func TestHelpCellSurvivesWarnings(t *testing.T) {
	cases := map[string]func(*App){
		"warning":  func(a *App) { a.warning = "⚠ kept the local host key for 10.0.0.1" },
		"error":    func(a *App) { a.errMsg = "connection refused"; a.rightMode = rightTerminal },
		"status":   func(a *App) { a.status = "synced · 12 servers · 2026-07-23 19:04" },
		"scanning": func(a *App) { a.scanning = true },
	}
	for name, setup := range cases {
		app := helpApp(t)
		setup(app)

		line := app.statusLine()
		if !strings.Contains(stripANSI(line), "? help") {
			t.Errorf("%s: the help key is missing from %q", name, stripANSI(line))
		}
		if got := ansi.StringWidth(padLine(line, app.width)); got != app.width {
			t.Errorf("%s: status line is %d wide, want %d", name, got, app.width)
		}
	}

	// A drag is the same story, in the split view.
	app := sftpApp(t, 120, 40)
	app.drag = &dragState{from: focusLocal, entries: []model.FileEntry{{Name: "main.go"}}}
	if !strings.Contains(stripANSI(app.statusLine()), "? help") {
		t.Errorf("dragging: the help key is missing from %q", stripANSI(app.statusLine()))
	}
}

func TestStatusHintNeverOverflows(t *testing.T) {
	for width := 20; width <= 200; width += 7 {
		app := helpApp(t)
		app.resize(width, 30)
		app.status = "a fairly long status message about something that just happened"

		line := app.statusLine()
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("width %d: status line is %d wide:\n%q", width, got, stripANSI(line))
		}
		// Wide enough to hold both: the cell must be there.
		if width >= 40 && !strings.Contains(stripANSI(line), "? help") {
			t.Fatalf("width %d: no help key in %q", width, stripANSI(line))
		}
	}
}

func TestHelpCellInSessionSaysCtrlB(t *testing.T) {
	app := helpApp(t)
	app.focus = focusSession
	app.rightMode = rightTerminal
	if got := stripANSI(app.statusLine()); !strings.Contains(got, escapeHint+" ? help") {
		t.Errorf("status line in a session: %q", got)
	}
}

func TestNoHelpCellOnModals(t *testing.T) {
	app := helpApp(t)
	app.confirm = &confirm{title: "Delete"}
	if got := stripANSI(app.statusLine()); strings.Contains(got, "? help") {
		t.Errorf("the help key is advertised over a dialog: %q", got)
	}
}

// TestWideStatusLineUnchanged: on a roomy terminal the message half is the same
// sentence v6 wrote by hand. Only the pinned cell is new.
func TestWideStatusLineUnchanged(t *testing.T) {
	app := helpApp(t)
	want := "tab focus panel · n new session · f files · " + escapeHint + " leave session · q quit"
	if got := stripANSI(app.statusMessage()); got != want {
		t.Errorf("default hint:\n got %q\nwant %q", got, want)
	}

	files := sftpApp(t, 140, 40)
	wantSFTP := "tab pane · space select · t send · d delete · R rename · r refresh · drag to transfer · " + escapeHint + " back"
	if got := stripANSI(files.statusMessage()); got != wantSFTP {
		t.Errorf("sftp hint:\n got %q\nwant %q", got, wantSFTP)
	}
}

// TestHelpProblemsAreReadableInTheCard: keys.json complaints are said once on
// the status line and then live where the user can read them.
func TestHelpProblemsAreReadableInTheCard(t *testing.T) {
	app := helpApp(t)
	app.applyKeymap(keymapLoadedMsg{keys: map[string][]string{"sidebar.explode": {"x"}}})
	if !strings.Contains(app.status, "keys.json") {
		t.Fatalf("nothing on the status line: %q", app.status)
	}
	app.openHelp()
	if body := stripANSI(app.helpView(78, 40)); !strings.Contains(body, "sidebar.explode") {
		t.Errorf("the card does not explain the problem:\n%s", body)
	}
}
