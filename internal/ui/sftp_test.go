package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	label   string
	dirs    map[string][]model.FileEntry
	removed []string
	renamed [2]string
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

// Stat answers from the same map List reads, so Plan walks the fake tree the
// way it would walk a real one.
func (f *fakeBrowser) Stat(p string) (model.FileEntry, bool, error) {
	name := p[strings.LastIndex(p, "/")+1:]
	if _, ok := f.dirs[p]; ok {
		return model.FileEntry{Name: name, IsDir: true}, true, nil
	}
	for _, e := range f.dirs[f.Parent(p)] {
		if e.Name == name {
			return e, true, nil
		}
	}
	return model.FileEntry{}, false, nil
}

func (f *fakeBrowser) Remove(p string, recursive bool) error {
	f.removed = append(f.removed, p)
	return nil
}

func (f *fakeBrowser) Rename(oldPath, newPath string) error {
	f.renamed = [2]string{oldPath, newPath}
	return nil
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

// settle runs a command and feeds its message back, which is what the runtime
// does. v3 puts a walk between "the user asked" and "the panel appears", so the
// tests have to take that step too.
func settle(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	for range 4 {
		if cmd == nil {
			return
		}
		msg := cmd()
		if msg == nil {
			return
		}
		var m tea.Model = app
		_, cmd = m.Update(msg)
	}
}

// askTransfer is buildTransfer plus the walk, i.e. everything up to the
// confirmation panel appearing.
func askTransfer(t *testing.T, app *App, from focusArea, entries ...model.FileEntry) {
	t.Helper()
	settle(t, app, app.buildTransfer(from, entries))
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
		askTransfer(t, app, focusLocal, model.FileEntry{Name: "main.go", Size: 1234})
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
	askTransfer(t, app, focusLocal, model.FileEntry{Name: "main.go", Size: 1234})

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
	// It returns the walk the drop started, which the runtime would run.
	release := func(x, y int) {
		settle(t, app, app.handleMouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone}))
	}

	press(localX, fileRow)
	if app.drag == nil || len(app.drag.entries) != 1 || app.drag.entries[0].Name != "main.go" {
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
	if !app.pending.upload || app.pending.dstPath() != "/srv/main.go" || len(app.pending.overwrite) != 1 {
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

	// ".." is navigation and never cargo. A directory is cargo now: v3 copies
	// recurse, so dragging a folder is the whole point.
	press(localX, paneBodyTop) // ".."
	if app.drag != nil {
		t.Error("“..” must not start a drag")
	}
	app.drag = nil
	press(localX, paneBodyTop+1) // docs/
	if app.drag == nil || app.drag.entries[0].Name != "docs" {
		t.Errorf("a directory should be draggable now, got %+v", app.drag)
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
	settle(t, app, app.handleMouse(tea.MouseMsg{X: sidebarWidth + localOuter + 2, Y: fileRow, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone}))
	if app.pending == nil {
		t.Fatal("the drag produced no request")
	}
	viaDrag := *app.pending
	app.pending = nil

	// Same file, reached with the keyboard.
	app.focus = focusLocal
	app.local.cursor = 2
	settle(t, app, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}))
	if app.pending == nil {
		t.Fatal("t produced no request")
	}
	if diff := requestDiff(*app.pending, viaDrag); diff != "" {
		t.Errorf("keyboard request differs from the drag's: %s", diff)
	}
	app.pending = nil

	// enter on a file does the same; on a directory it navigates instead.
	settle(t, app, app.handleKey(tea.KeyMsg{Type: tea.KeyEnter}))
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

	// t on a directory copies it, recursively — that is what v3 added.
	settle(t, app, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}))
	if app.pending == nil {
		t.Fatal("t on a directory should offer a recursive transfer")
	}
	if app.pending.dirs == 0 {
		t.Errorf("the plan counted no directories: %+v", *app.pending)
	}
}

// requestDiff compares the fields that say what would move. transferReq holds
// slices, so it cannot simply be compared with ==.
func requestDiff(a, b transferReq) string {
	switch {
	case a.upload != b.upload:
		return fmt.Sprintf("upload %v vs %v", a.upload, b.upload)
	case a.srcDir != b.srcDir || a.dstDir != b.dstDir:
		return fmt.Sprintf("dirs %s→%s vs %s→%s", a.srcDir, a.dstDir, b.srcDir, b.dstDir)
	case a.srcPath() != b.srcPath() || a.dstPath() != b.dstPath():
		return fmt.Sprintf("paths %s→%s vs %s→%s", a.srcPath(), a.dstPath(), b.srcPath(), b.dstPath())
	case len(a.entries) != len(b.entries):
		return fmt.Sprintf("%d entries vs %d", len(a.entries), len(b.entries))
	case a.total != b.total || a.files != b.files || a.dirs != b.dirs:
		return fmt.Sprintf("totals %+v vs %+v", a, b)
	case len(a.overwrite) != len(b.overwrite):
		return fmt.Sprintf("overwrite %v vs %v", a.overwrite, b.overwrite)
	}
	return ""
}

// TestMarkedTransferIncludesAllSelected: space picks rows, and every way of
// starting a transfer then moves the whole selection.
func TestMarkedTransferIncludesAllSelected(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.remote = &sftppkg.Remote{}

	// space marks main.go and notes.txt, moving down as it goes.
	app.local.cursor = 2
	app.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	app.handleKey(tea.KeyMsg{Type: tea.KeySpace})
	if len(app.local.marked) != 2 {
		t.Fatalf("marked %v, want main.go and notes.txt", app.local.marked)
	}
	if app.pending != nil {
		t.Error("space must not start a transfer any more — it selects")
	}

	settle(t, app, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}))
	if app.pending == nil {
		t.Fatal("t produced no request")
	}
	viaKey := *app.pending
	if len(viaKey.entries) != 2 {
		t.Errorf("t moved %d entries, want both selected: %+v", len(viaKey.entries), viaKey.entries)
	}
	app.pending = nil

	// Dragging one of the selected rows takes the selection with it.
	localOuter, _ := app.sftpWidths()
	fileRow := paneBodyTop + 2 // main.go, which is marked
	app.handleMouse(tea.MouseMsg{X: sidebarWidth + 2, Y: fileRow, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if app.drag == nil || len(app.drag.entries) != 2 {
		t.Fatalf("dragging a selected row should carry the selection, got %+v", app.drag)
	}
	app.handleMouse(tea.MouseMsg{X: sidebarWidth + localOuter + 2, Y: fileRow, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
	settle(t, app, app.handleMouse(tea.MouseMsg{X: sidebarWidth + localOuter + 2, Y: fileRow, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone}))
	if app.pending == nil {
		t.Fatal("the drag produced no request")
	}
	if diff := requestDiff(*app.pending, viaKey); diff != "" {
		t.Errorf("the drag differs from the keyboard: %s", diff)
	}
	app.pending = nil

	// An unselected row drags alone and leaves the selection alone.
	app.handleMouse(tea.MouseMsg{X: sidebarWidth + 2, Y: paneBodyTop + 1, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if app.drag == nil || len(app.drag.entries) != 1 || app.drag.entries[0].Name != "docs" {
		t.Errorf("an unselected row should drag alone, got %+v", app.drag)
	}
	if len(app.local.marked) != 2 {
		t.Error("dragging an unselected row cleared the selection")
	}

	// a clears it; a new listing does too.
	app.drag = nil
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if len(app.local.marked) != 0 {
		t.Errorf("a should clear the selection, got %v", app.local.marked)
	}
	app.local.marked = map[string]bool{"main.go": true}
	app.local.setEntries("/elsewhere", nil)
	if len(app.local.marked) != 0 {
		t.Error("a new listing must drop the selection — the names mean another directory now")
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

// TestBusyBlocksASecondTransfer: one transfer at a time is still the rule in
// v3 — a queue is a later version's problem.
func TestBusyBlocksASecondTransfer(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.remote = &sftppkg.Remote{}
	app.transfer = &transferState{prog: &sftppkg.Progress{}, cancel: func() {}, label: "x", started: time.Now()}

	askTransfer(t, app, focusLocal, model.FileEntry{Name: "main.go", Size: 1234})
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
	askTransfer(t, app, focusLocal, model.FileEntry{Name: "main.go", Size: 1234})

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

// TestTransferProgressKeepsLayout: the progress bar takes the width that is
// left, never more — the frame is still one exact rectangle while a copy runs.
func TestTransferProgressKeepsLayout(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {60, 20}, {40, 12}, {200, 60}} {
		width, height := size[0], size[1]
		app := sftpApp(t, width, height)
		app.remote = &sftppkg.Remote{}

		prog := &sftppkg.Progress{}
		prog.SetTotal(1000)
		prog.SetName("app.tar.gz")
		app.transfer = &transferState{
			prog:    prog,
			cancel:  func() {},
			label:   "app.tar.gz",
			upload:  true,
			started: time.Now().Add(-2 * time.Second),
		}

		// Both ends of the range: an unknown total draws an empty track, a known
		// one draws a bar, and neither may change the frame.
		for _, total := range []int64{0, 1000} {
			prog.SetTotal(total)
			lines := strings.Split(app.View(), "\n")
			if len(lines) != height {
				t.Fatalf("%dx%d: %d rows, want %d", width, height, len(lines), height)
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != width {
					t.Errorf("%dx%d total=%d: row %d is %d wide, want %d", width, height, total, i, w, width)
				}
			}
			// The panes are still standing under the bar.
			if got := strings.Count(stripANSI(lines[topMargin]), "╭"); got != 3 {
				t.Errorf("%dx%d: %d panel tops during a transfer, want 3", width, height, got)
			}
		}
	}
}

// TestProgressBarIsExactlyItsWidth: the bar is drawn by hand precisely so its
// width is ours to guarantee, at any fill and with no total at all.
func TestProgressBarIsExactlyItsWidth(t *testing.T) {
	for _, cols := range []int{1, 8, 20, 77} {
		for _, done := range []int64{0, 1, 499, 1000, 5000} {
			for _, total := range []int64{0, 1000} {
				got := progressBar(cols, done, total)
				if w := ansi.StringWidth(got); w != cols {
					t.Errorf("progressBar(%d, %d, %d) is %d wide", cols, done, total, w)
				}
			}
		}
	}
	if strings.Contains(stripANSI(progressBar(10, 500, 0)), "▓") {
		t.Error("an unknown total must not draw a filled bar — it would be a guess")
	}
	if got := strings.Count(stripANSI(progressBar(10, 500, 1000)), "▓"); got != 5 {
		t.Errorf("half done should fill half the bar, got %d of 10", got)
	}
}

// TestCtrlCCancelsTransferNotApp: while a copy runs, ctrl+c stops the copy. It
// must not quit — losing the whole app to a keystroke meant for one transfer is
// exactly what the binding is there to prevent.
func TestCtrlCCancelsTransferNotApp(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.remote = &sftppkg.Remote{}

	cancelled := false
	app.transfer = &transferState{
		prog:    &sftppkg.Progress{},
		cancel:  func() { cancelled = true },
		label:   "big.bin",
		started: time.Now(),
	}

	if cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Error("ctrl+c during a transfer must not quit the app")
	}
	if !cancelled {
		t.Fatal("ctrl+c did not cancel the transfer")
	}
	if app.transfer == nil {
		t.Fatal("the transfer state must survive until the goroutine reports back")
	}
	if !app.transfer.cancelling {
		t.Error("the status line should say it is cancelling")
	}
	if !strings.Contains(stripANSI(app.statusLine()), "cancelling") {
		t.Errorf("status line: %q", stripANSI(app.statusLine()))
	}

	// The goroutine answering is what actually clears it, and a cancellation is
	// reported as an answer rather than an error.
	var m tea.Model = app
	m.Update(transferDoneMsg{gen: app.sftpGen, label: "big.bin", err: sftppkg.ErrCancelled})
	if app.transfer != nil {
		t.Error("the done message should clear the transfer")
	}
	if app.errMsg != "" || !strings.Contains(app.status, "cancelled") {
		t.Errorf("cancelling is not a failure: err=%q status=%q", app.errMsg, app.status)
	}

	// With nothing running, ctrl+c is the quit key again. (The zero-value
	// Remote here has no connection to close, so it is dropped first.)
	app.remote = nil
	app.focus = focusSidebar
	if cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Error("ctrl+c with no transfer should still quit")
	}
}

// TestRenameSwallowsKeys: the one-line editor owns the keyboard, like every
// other question in this app.
func TestRenameSwallowsKeys(t *testing.T) {
	app := sftpApp(t, 100, 24)
	app.local.cursor = 2 // main.go

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	if app.rename == nil {
		t.Fatal("R should open the rename input")
	}
	if got := app.rename.input.Value(); got != "main.go" {
		t.Errorf("the input should start from the current name, got %q", got)
	}
	if !strings.Contains(stripANSI(app.View()), "Rename") {
		t.Error("the rename card is not on screen")
	}

	before := app.local.cursor
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyDown},
		{Type: tea.KeyTab},
		{Type: tea.KeyCtrlB},
		{Type: tea.KeyRunes, Runes: []rune("t")},
	} {
		app.handleKey(k)
		if app.rename == nil {
			t.Fatalf("%v dismissed the rename input", k)
		}
		if app.local.cursor != before || app.focus != focusLocal || app.pending != nil {
			t.Errorf("%v leaked into the panes", k)
		}
	}

	// A path separator is refused rather than silently moving the file.
	app.rename.input.SetValue("sub/main.go")
	app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if app.rename == nil || app.rename.err == "" {
		t.Fatal("a name with a separator should be refused, not accepted")
	}

	app.rename.input.SetValue("renamed.go")
	cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if app.rename != nil {
		t.Fatal("enter should close the input")
	}
	if cmd == nil {
		t.Fatal("enter should produce the rename command")
	}
	cmd()
	br := app.local.br.(*fakeBrowser)
	if br.renamed != [2]string{"/home/main.go", "/home/renamed.go"} {
		t.Errorf("renamed %v", br.renamed)
	}

	// esc leaves everything as it was.
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if app.rename != nil {
		t.Error("esc should close the input")
	}
}

// TestDeleteCountsBeforeAsking: a recursive delete must never be one keystroke,
// so the confirmation says how much is inside.
func TestDeleteCountsBeforeAsking(t *testing.T) {
	app := sftpApp(t, 100, 24)
	// docs/ has two files in it, which is what the confirmation has to report.
	br := app.local.br.(*fakeBrowser)
	br.dirs["/home/docs"] = []model.FileEntry{
		{Name: "a.md", Size: 1},
		{Name: "b.md", Size: 2},
	}

	app.local.cursor = 1 // docs/
	settle(t, app, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")}))
	if app.confirm == nil {
		t.Fatal("d should ask before deleting")
	}
	body := stripANSI(app.View())
	for _, want := range []string{"Delete docs?", "1 directory, 2 files"} {
		if !strings.Contains(body, want) {
			t.Errorf("the confirmation is missing %q:\n%s", want, body)
		}
	}

	cmd, handled := app.confirm.resolve(app.keys, tea.KeyMsg{Type: tea.KeyEnter})
	if !handled || cmd == nil {
		t.Fatal("enter should confirm the delete")
	}
	cmd()
	if len(br.removed) != 1 || br.removed[0] != "/home/docs" {
		t.Errorf("removed %v, want /home/docs", br.removed)
	}
}
