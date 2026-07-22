package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
	sftppkg "github.com/pyjhoop/ssh-client/internal/sftp"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// fakeBrowser stands in for a remote: the UI tests are about geometry and state
// transitions, and neither needs a live connection.
type fakeBrowser struct {
	label string
	dirs  map[string][]model.FileEntry
}

func (f *fakeBrowser) Label() string                { return f.label }
func (f *fakeBrowser) Join(dir, name string) string { return strings.TrimSuffix(dir, "/") + "/" + name }
func (f *fakeBrowser) Parent(dir string) string {
	if dir == "/" {
		return "/"
	}
	idx := strings.LastIndex(strings.TrimSuffix(dir, "/"), "/")
	if idx <= 0 {
		return "/"
	}
	return dir[:idx]
}
func (f *fakeBrowser) Home() (string, error) { return "/home", nil }
func (f *fakeBrowser) List(dir string) ([]model.FileEntry, error) {
	return f.dirs[dir], nil
}

// sftpApp builds a root model already in the split view, with both panes filled
// from fakeBrowsers. remote stays nil, so tests that need a real transfer must
// set it themselves.
func sftpApp(t *testing.T, width, height int) *App {
	t.Helper()

	app := New(config.New(t.TempDir()))
	app.servers = []model.Server{{ID: "1", Name: "alpha", Host: "a", User: "u", Port: 22}}
	app.sidebar.SetServers(app.servers)
	app.resize(width, height)

	localFiles := []model.FileEntry{
		{Name: "docs", IsDir: true},
		{Name: "main.go", Size: 1234},
		{Name: "notes.txt", Size: 20},
	}
	remoteFiles := []model.FileEntry{
		{Name: "app", IsDir: true},
		{Name: "deploy.sh", Size: 512},
		{Name: "main.go", Size: 99}, // same name: drives the overwrite warning
	}

	app.rightMode = rightSFTP
	app.focus = focusLocal
	app.sftpID = "1"
	app.sftpName = "alpha"
	app.sftpAddr = "u@a:22"

	app.local.br = &fakeBrowser{label: "Local", dirs: map[string][]model.FileEntry{"/home": localFiles}}
	app.remotePane.br = &fakeBrowser{label: "u@a", dirs: map[string][]model.FileEntry{"/srv": remoteFiles}}
	app.local.setEntries("/home", localFiles)
	app.remotePane.setEntries("/srv", remoteFiles)
	return app
}

// TestSFTPLayoutAlignment is TestLayoutAlignment for the three-panel view: the
// frame must still be one exact rectangle, now with three borders per row.
func TestSFTPLayoutAlignment(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {80, 30}, {200, 60}, {40, 10}} {
		width, height := size[0], size[1]
		app := sftpApp(t, width, height)

		lines := strings.Split(app.View(), "\n")
		if len(lines) != height {
			t.Errorf("%dx%d: rendered %d rows, want %d", width, height, len(lines), height)
			continue
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w != width {
				t.Errorf("%dx%d: row %d is %d columns wide, want %d", width, height, i, w, width)
			}
		}
		top, bottom := lines[topMargin], lines[height-statusRows-1]
		if got := strings.Count(stripANSI(top), "╭"); got != 3 {
			t.Errorf("%dx%d: %d panel tops on row %d, want 3: %q", width, height, got, topMargin, stripANSI(top))
		}
		if got := strings.Count(stripANSI(bottom), "╰"); got != 3 {
			t.Errorf("%dx%d: %d panel bottoms, want 3: %q", width, height, got, stripANSI(bottom))
		}
	}
}

// TestSFTPModalFloatsOverThePanes is the property the split view's dialogs
// depend on: the three panels stay standing and the card is spliced on top,
// with every row still exactly as wide as the terminal.
func TestSFTPModalFloatsOverThePanes(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {80, 30}, {200, 60}, {40, 12}} {
		width, height := size[0], size[1]

		app := sftpApp(t, width, height)
		app.local.cursor = 2 // main.go
		app.remote = &sftppkg.Remote{}
		app.buildTransfer(focusLocal, model.FileEntry{Name: "main.go", Size: 1234})
		if app.pending == nil {
			t.Fatalf("%dx%d: buildTransfer produced no pending transfer", width, height)
		}

		lines := strings.Split(app.View(), "\n")
		if len(lines) != height {
			t.Errorf("%dx%d: rendered %d rows, want %d", width, height, len(lines), height)
			continue
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w != width {
				t.Errorf("%dx%d: row %d is %d columns wide, want %d", width, height, i, w, width)
			}
		}

		// The panes are untouched: their tops and bottoms are still all three.
		if got := strings.Count(stripANSI(lines[topMargin]), "╭"); got != 3 {
			t.Errorf("%dx%d: %d panel tops, want 3 — the modal replaced a panel", width, height, got)
		}
		if got := strings.Count(stripANSI(lines[height-statusRows-1]), "╰"); got != 3 {
			t.Errorf("%dx%d: %d panel bottoms, want 3", width, height, got)
		}

		body := stripANSI(strings.Join(lines, "\n"))
		if !strings.Contains(body, "Upload file") {
			t.Errorf("%dx%d: the alert is not on screen", width, height)
		}
		// The rest only fits where there is room; a 12-row terminal clips it,
		// which is a limit of the frame rather than a layout bug.
		if height < 20 {
			continue
		}
		for _, want := range []string{"/home/main.go", "/srv/main.go", "overwritten"} {
			if !strings.Contains(body, want) {
				t.Errorf("%dx%d: the alert is missing %q", width, height, want)
			}
		}
	}
}

// TestSFTPModalWithColour is the same check with colours actually turned on.
// Under `go test` stdout is not a terminal, so lipgloss strips every escape and
// the plain runs above never exercise the splice on a styled row — which is the
// only situation where the width arithmetic can go wrong.
func TestSFTPModalWithColour(t *testing.T) {
	before := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(before) })

	app := sftpApp(t, 100, 24)
	app.remote = &sftppkg.Remote{}
	app.local.cursor = 2
	app.buildTransfer(focusLocal, model.FileEntry{Name: "main.go", Size: 1234})

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
		t.Errorf("%d panel tops, want 3", got)
	}
}

// TestOverlayPreservesWidthAndStyle pins the splice primitive itself: it is the
// only place a styled row is cut, and a cut that drops or leaks an SGR sequence
// would corrupt every row after it.
func TestOverlayPreservesWidthAndStyle(t *testing.T) {
	// The escapes are written out literally rather than through a lipgloss
	// style: under `go test` stdout is not a terminal, so lipgloss would strip
	// the very colours this test is about.
	const (
		red    = "\x1b[31m"
		yellow = "\x1b[33m"
	)
	base := red + "LLLLLLLLLL" + ansiReset + yellow + "RRRRRRRRRR" + ansiReset
	if w := ansi.StringWidth(base); w != 20 {
		t.Fatalf("base row is %d wide, want 20", w)
	}
	box := "[BOX]"

	got := overlay(base, box, 7, 0)
	if w := ansi.StringWidth(got); w != 20 {
		t.Errorf("spliced row is %d wide, want 20", w)
	}
	if want := "LLLLLLL[BOX]RRRRRRRR"; stripANSI(got) != want {
		t.Errorf("spliced text: got %q, want %q", stripANSI(got), want)
	}

	head, tail, found := strings.Cut(got, box)
	if !found {
		t.Fatalf("the box is not in the result: %q", got)
	}
	// The kept text on each side must still carry the colour it had, or every
	// panel to the right of the dialog would render unstyled.
	if !strings.Contains(head, red) {
		t.Errorf("the text before the box lost its colour: %q", head)
	}
	if !strings.Contains(tail, yellow) {
		t.Errorf("the text after the box lost its colour: %q", tail)
	}
	// And the box itself must not inherit either of them.
	if idx := strings.LastIndex(head, "\x1b["); idx < 0 || !strings.HasPrefix(head[idx:], ansiReset) {
		t.Errorf("the box is not preceded by a reset: %q", head)
	}

	// Out-of-range placements are dropped rather than clipped, so a box can
	// never widen a row.
	for _, x := range []int{-1, 16, 100} {
		if w := ansi.StringWidth(overlay(base, box, x, 0)); w != 20 {
			t.Errorf("overlay at x=%d changed the width to %d", x, w)
		}
	}
	if w := ansi.StringWidth(overlay(base, box, 7, 5)); w != 20 {
		t.Error("a box placed past the last row should be dropped")
	}
}

// TestSFTPErrorCardFloatsAndDismisses: a failed connection must not take the
// panes down — the local side is still worth looking at.
func TestSFTPErrorCardFloatsAndDismisses(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.lastAttempt = model.Server{ID: "1", Name: "alpha", Host: "10.0.0.1", Port: 22, User: "deploy"}
	app.lastWasSFTP = true

	var m tea.Model = app
	m, _ = m.Update(sftpFailedMsg{gen: app.sftpGen, err: fmt.Errorf("%w: nope", sshpkg.ErrSFTP)})

	if app.rightMode != rightSFTP {
		t.Fatalf("a failed sftp connect must keep the split view, rightMode=%v", app.rightMode)
	}
	view := app.View()
	for i, l := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(l); w != 100 {
			t.Errorf("row %d is %d columns wide, want 100", i, w)
		}
	}
	lines := strings.Split(view, "\n")
	body := stripANSI(view)
	if !strings.Contains(body, "sftp unavailable") {
		t.Error("the error card should be on screen")
	}
	// The panes are still standing behind it — the card is wide enough to hide
	// the local one's text, but never its frame.
	if got := strings.Count(stripANSI(lines[topMargin]), "╭"); got != 3 {
		t.Errorf("%d panel tops behind the card, want 3", got)
	}
	if !strings.Contains(body, "512 B") {
		t.Error("the remote pane should still be visible beside the card")
	}

	// The card owns its keys while it is up: navigation must not leak through.
	before := app.local.cursor
	app.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if app.local.cursor != before {
		t.Error("the pane moved while the error card was up")
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if app.sftpErr != nil {
		t.Error("esc should dismiss the card")
	}
	if !strings.Contains(stripANSI(app.View()), "notes.txt") {
		t.Error("dismissing should leave the panes behind it intact")
	}

	// A stale failure from a superseded connection is ignored.
	app.sftpGen++
	m, _ = m.Update(sftpFailedMsg{gen: app.sftpGen - 1, err: fmt.Errorf("%w: old", sshpkg.ErrSFTP)})
	if app.sftpErr != nil {
		t.Error("a superseded failure should be dropped")
	}
}

// TestFilePaneRowGeometry checks rowToIndex against what View actually draws,
// scroll offset included — the drop coordinate mapping depends on it.
func TestFilePaneRowGeometry(t *testing.T) {
	app := sftpApp(t, 100, 24)
	lines := strings.Split(app.View(), "\n")

	// entries[0] is "..", so the files start at index 1.
	for wantIdx, label := range []string{"..", "docs/", "main.go", "notes.txt"} {
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
		got, ok := app.local.rowToIndex(row)
		if !ok || got != wantIdx {
			t.Errorf("%q renders on row %d; rowToIndex gave (%d, %v), want %d", label, row, got, ok, wantIdx)
		}
	}

	// A scrolled pane shifts the mapping by exactly the offset.
	app.local.offset = 2
	if got, ok := app.local.rowToIndex(paneBodyTop); !ok || got != 2 {
		t.Errorf("scrolled rowToIndex(%d) = (%d, %v), want 2", paneBodyTop, got, ok)
	}
	// Rows outside the body belong to nobody.
	if _, ok := app.local.rowToIndex(paneBodyTop - 1); ok {
		t.Error("the title bar row should not map to an entry")
	}
	if _, ok := app.local.rowToIndex(paneBodyTop + app.local.rows); ok {
		t.Error("the row below the body should not map to an entry")
	}
}

// TestDragDropRequestsConfirmation walks the three mouse stages and the two
// ways a drag can end without doing anything.
func TestDragDropRequestsConfirmation(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.remote = &sftppkg.Remote{}
	localOuter, _ := app.sftpWidths()
	localX := sidebarWidth + 2
	remoteX := sidebarWidth + localOuter + 2

	// main.go is entries[2] (".." docs main.go), so it renders two rows down.
	fileRow := paneBodyTop + 2

	press := func(x, y int) {
		app.handleMouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	}
	motion := func(x, y int) {
		app.handleMouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	}
	// Release deliberately reports no button: several terminals do exactly that.
	release := func(x, y int) {
		app.handleMouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone})
	}

	press(localX, fileRow)
	if app.drag == nil || app.drag.entry.Name != "main.go" {
		t.Fatalf("press on a file should start a drag, got %+v", app.drag)
	}
	if app.focus != focusLocal {
		t.Errorf("press should focus the pane, focus=%v", app.focus)
	}

	motion(remoteX, fileRow)
	if app.drag.over != focusRemote {
		t.Errorf("motion should record the pane under the pointer, over=%v", app.drag.over)
	}

	release(remoteX, fileRow)
	if app.drag != nil {
		t.Error("release should end the drag")
	}
	if app.pending == nil {
		t.Fatal("dropping on the other pane should ask for confirmation")
	}
	if !app.pending.upload || app.pending.dstPath != "/srv/main.go" || !app.pending.overwrite {
		t.Errorf("pending: %+v", *app.pending)
	}

	// esc cancels and leaves nothing behind.
	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if app.pending != nil {
		t.Error("esc should cancel the pending transfer")
	}

	// Dropping inside the same pane does nothing at all.
	press(localX, fileRow)
	motion(localX, fileRow+1)
	release(localX, fileRow+1)
	if app.pending != nil || app.drag != nil {
		t.Errorf("a same-pane drop must not transfer: pending=%+v drag=%+v", app.pending, app.drag)
	}

	// Neither does dropping on the sidebar.
	press(localX, fileRow)
	motion(1, fileRow)
	release(1, fileRow)
	if app.pending != nil {
		t.Error("dropping on the sidebar must not transfer")
	}

	// Directories and ".." are navigation, not cargo.
	press(localX, paneBodyTop) // ".."
	if app.drag != nil {
		t.Error("“..” must not start a drag")
	}
	press(localX, paneBodyTop+1) // docs/
	if app.drag != nil {
		t.Error("a directory must not start a drag")
	}
}

// TestKeyboardTransferMatchesDrag is the promise that both entry points funnel
// through buildTransfer: same file, same request.
func TestKeyboardTransferMatchesDrag(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.remote = &sftppkg.Remote{}
	localOuter, _ := app.sftpWidths()
	fileRow := paneBodyTop + 2

	app.handleMouse(tea.MouseMsg{X: sidebarWidth + 2, Y: fileRow, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	app.handleMouse(tea.MouseMsg{X: sidebarWidth + localOuter + 2, Y: fileRow, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	app.handleMouse(tea.MouseMsg{X: sidebarWidth + localOuter + 2, Y: fileRow, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone})
	if app.pending == nil {
		t.Fatal("the drag produced no request")
	}
	viaDrag := *app.pending
	app.pending = nil

	// Same file, reached with the keyboard.
	app.focus = focusLocal
	app.local.cursor = 2
	app.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	if app.pending == nil {
		t.Fatal("space produced no request")
	}
	if viaKey := *app.pending; viaKey != viaDrag {
		t.Errorf("keyboard request %+v differs from the drag's %+v", viaKey, viaDrag)
	}
	app.pending = nil

	// enter on a file does the same; on a directory it navigates instead.
	app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if app.pending == nil {
		t.Error("enter on a file should ask to transfer it")
	}
	app.pending = nil

	app.local.cursor = 1 // docs/
	if cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Error("enter on a directory should produce a listing command")
	}
	if app.pending != nil {
		t.Error("enter on a directory must not offer a transfer")
	}

	// space on a directory is refused outright rather than navigating.
	app.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	if app.pending != nil {
		t.Error("space on a directory must not offer a transfer")
	}
	if !strings.Contains(app.errMsg, "not supported") {
		t.Errorf("expected a refusal message, got %q", app.errMsg)
	}
}

// TestSFTPKeyRouting covers navigation, pane switching and the escape that keeps
// the connection alive.
func TestSFTPKeyRouting(t *testing.T) {
	app := sftpApp(t, 100, 24)

	app.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	if app.local.cursor != 1 {
		t.Errorf("down: cursor=%d, want 1", app.local.cursor)
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	app.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if app.local.cursor != 0 {
		t.Errorf("up should clamp at the top, cursor=%d", app.local.cursor)
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyEnd})
	if want := len(app.local.entries) - 1; app.local.cursor != want {
		t.Errorf("end: cursor=%d, want %d", app.local.cursor, want)
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if app.focus != focusRemote {
		t.Errorf("tab should switch panes, focus=%v", app.focus)
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if app.focus != focusLocal {
		t.Errorf("tab should switch back, focus=%v", app.focus)
	}

	// ctrl+b leaves the panes focused but connected, exactly like the session.
	app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if app.focus != focusSidebar {
		t.Errorf("ctrl+b should return to the sidebar, focus=%v", app.focus)
	}
	if app.rightMode != rightSFTP {
		t.Errorf("ctrl+b must not close the panes, rightMode=%v", app.rightMode)
	}
	// tab goes back into the split view.
	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	if app.focus != focusLocal {
		t.Errorf("tab from the sidebar should re-enter the panes, focus=%v", app.focus)
	}
}

// TestOpenSFTPIgnoresTheConnectRow: f on "+ Connect" has no server to browse.
func TestOpenSFTPIgnoresTheConnectRow(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.servers = []model.Server{{ID: "1", Name: "alpha", Host: "a", User: "u", Port: 22, Auth: model.AuthPassword, Password: "p"}}
	app.sidebar.SetServers(app.servers)
	app.resize(100, 24)

	if cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")}); cmd != nil {
		t.Error("f on “+ Connect” should do nothing")
	}
	if app.rightMode == rightSFTP {
		t.Fatal("f on “+ Connect” should not open the split view")
	}

	app.sidebar.list.Select(1)
	cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if cmd == nil {
		t.Fatal("f on a server should start connecting")
	}
	if app.rightMode != rightSFTP || app.focus != focusLocal {
		t.Fatalf("rightMode=%v focus=%v, want the split view", app.rightMode, app.focus)
	}
	if app.local.rows != app.panelHeight()-rightHeaderRows {
		t.Errorf("pane body height %d, want %d", app.local.rows, app.panelHeight()-rightHeaderRows)
	}
}

// TestPaneListingUpdates drives the pane through the message it actually
// receives, including the "root has no .." rule and the error path.
func TestPaneListingUpdates(t *testing.T) {
	app := sftpApp(t, 100, 24)
	var m tea.Model = app

	m, _ = m.Update(listedMsg{gen: app.sftpGen, side: focusRemote, dir: "/",
		entries: []model.FileEntry{{Name: "etc", IsDir: true}}})
	if len(app.remotePane.entries) != 1 || app.remotePane.entries[0].Name != "etc" {
		t.Errorf("the root listing should have no “..”, got %+v", app.remotePane.entries)
	}

	// A listing from a superseded connection is dropped.
	m, _ = m.Update(listedMsg{gen: app.sftpGen - 1, side: focusRemote, dir: "/gone",
		entries: []model.FileEntry{{Name: "stale"}}})
	if app.remotePane.dir != "/" {
		t.Errorf("a stale listing was applied, dir=%q", app.remotePane.dir)
	}

	// A failed listing shows in the pane, not as a crash.
	m, _ = m.Update(listedMsg{gen: app.sftpGen, side: focusRemote, dir: "/root", err: errPermission{}})
	if app.remotePane.err == "" {
		t.Error("a listing error should be shown in the pane")
	}
	if got := stripANSI(app.View()); !strings.Contains(got, "permission denied") {
		t.Errorf("the pane should render its error, view:\n%s", got)
	}
}

type errPermission struct{}

func (errPermission) Error() string { return "permission denied" }

// TestBusyBlocksASecondTransfer: v2 runs one transfer at a time.
func TestBusyBlocksASecondTransfer(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.remote = &sftppkg.Remote{}
	app.busy = true
	app.buildTransfer(focusLocal, model.FileEntry{Name: "main.go", Size: 1})
	if app.pending != nil {
		t.Error("a second transfer should be refused while one is running")
	}
	if !strings.Contains(app.errMsg, "already running") {
		t.Errorf("expected a busy message, got %q", app.errMsg)
	}
}

// TestPendingSwallowsKeys is the containment property the confirm panel has:
// nothing reaches the panes while the alert is up.
func TestPendingSwallowsKeys(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.remote = &sftppkg.Remote{}
	app.buildTransfer(focusLocal, model.FileEntry{Name: "main.go", Size: 1})

	before := app.local.cursor
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyDown},
		{Type: tea.KeyRunes, Runes: []rune("x")},
		{Type: tea.KeyCtrlB},
		{Type: tea.KeyTab},
	} {
		if cmd := app.handleKey(k); cmd != nil {
			t.Errorf("%v produced a command while the alert was up", k)
		}
		if app.pending == nil {
			t.Fatalf("%v dismissed the alert", k)
		}
		if app.local.cursor != before || app.focus != focusLocal {
			t.Errorf("%v leaked into the panes", k)
		}
	}
}
