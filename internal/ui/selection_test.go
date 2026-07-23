package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// The session panel's body starts below the frame's top margin, the panel border
// and its title bar, and to the right of the sidebar and its border. The mouse
// tests press at real screen coordinates so the geometry itself is under test.
const (
	sessionOriginX = sidebarWidth + 1
	sessionOriginY = topMargin + 1 + rightHeaderRows
)

// selectApp is an app with one live tab holding lines of known text.
func selectApp(t *testing.T, lines ...string) (*App, *sessionTab) {
	t.Helper()
	app := testApp(t)
	tab := attachTab(app, "web-1")
	app.focus = focusSession
	for _, l := range lines {
		fmt.Fprint(tab.emu(), l+"\r\n")
	}
	return app, tab
}

func press(app *App, x, y int) tea.Cmd {
	return app.handleMouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
}

func motion(app *App, x, y int) tea.Cmd {
	return app.handleMouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft})
}

// release reports no button on purpose: several terminals do exactly that, and
// v3 already learned to judge by whether a drag was in flight.
func release(app *App, x, y int) tea.Cmd {
	return app.handleMouse(tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone})
}

// drag runs the whole three-stage gesture in body coordinates.
func drag(app *App, x1, y1, x2, y2 int) tea.Cmd {
	press(app, sessionOriginX+x1, sessionOriginY+y1)
	motion(app, sessionOriginX+x2, sessionOriginY+y2)
	return release(app, sessionOriginX+x2, sessionOriginY+y2)
}

// runCmd runs a command and feeds its message back, the way the runtime would.
func runCmd(app *App, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if msg := cmd(); msg != nil {
		app.Update(msg)
	}
}

// ── selection rendering ─────────────────────────────────────────────────────

// TestSelectionRendersReversed: the selected cells are drawn reversed and the
// rest of the screen is untouched. The flip is done on the emulator's own cells
// and undone after the render — there is no shadow buffer of the selection.
func TestSelectionRendersReversed(t *testing.T) {
	app, tab := selectApp(t, "hello world")
	cols, rows := app.rightInner()

	before := renderScrolled(tab.emu(), cols, rows, 0, false)
	tab.sel = &selection{anchor: point{0, 0}, cursor: point{4, 0}}
	got := renderSelected(tab.emu(), cols, rows, 0, tab.sel, false)

	if !strings.Contains(got, "\x1b[7m") {
		t.Errorf("no reverse attribute in the rendered selection:\n%q", firstLine(got))
	}
	if !strings.Contains(stripANSI(got), "hello world") {
		t.Errorf("the text itself changed:\n%q", firstLine(stripANSI(got)))
	}

	// And the cells are put back: the next frame looks like the first one.
	tab.sel = nil
	if after := renderScrolled(tab.emu(), cols, rows, 0, false); after != before {
		t.Error("the screen did not come back after the selection was dropped")
	}
}

// TestSelectionIsLinearNotBlock: a selection spanning rows takes the tail of the
// first line, all of the middle ones and the head of the last — not a rectangle.
func TestSelectionIsLinearNotBlock(t *testing.T) {
	app, tab := selectApp(t, "aaaa1111", "bbbbbbbb", "2222cccc")
	cols, rows := app.rightInner()

	tab.sel = &selection{anchor: point{4, 0}, cursor: point{3, 2}}
	got := selectedText(tab.emu(), cols, rows, 0, tab.sel)

	want := "1111\nbbbbbbbb\n2222"
	if got != want {
		t.Errorf("linear selection:\n got %q\nwant %q", got, want)
	}
}

// TestSelectionNormalizesBackwardDrag: dragging bottom-right to top-left copies
// the same text as the same drag the other way.
func TestSelectionNormalizesBackwardDrag(t *testing.T) {
	app, tab := selectApp(t, "one", "two")
	cols, rows := app.rightInner()

	forward := selectedText(tab.emu(), cols, rows, 0, &selection{anchor: point{0, 0}, cursor: point{2, 1}})
	backward := selectedText(tab.emu(), cols, rows, 0, &selection{anchor: point{2, 1}, cursor: point{0, 0}})
	if forward != backward {
		t.Errorf("direction changed the text:\n forward  %q\n backward %q", forward, backward)
	}
	if forward != "one\ntwo" {
		t.Errorf("selected text is %q", forward)
	}
}

// TestTrailingSpacesAreStripped: a selection that runs past the end of a line
// copies the words, not the padding the terminal draws after them.
func TestTrailingSpacesAreStripped(t *testing.T) {
	app, tab := selectApp(t, "short")
	cols, rows := app.rightInner()

	got := selectedText(tab.emu(), cols, rows, 0, &selection{anchor: point{0, 0}, cursor: point{cols - 1, 0}})
	if got != "short" {
		t.Errorf("selected %q, want %q", got, "short")
	}
}

// TestSoftWrappedLinesAreNotJoined: a line too long for the panel is two rows on
// screen and two lines in the clipboard. vt does not record where it wrapped, so
// rejoining would splice text back together at a guess.
func TestSoftWrappedLinesAreNotJoined(t *testing.T) {
	app, tab := selectApp(t)
	cols, rows := app.rightInner()
	fmt.Fprint(tab.emu(), strings.Repeat("x", cols+5))

	got := selectedText(tab.emu(), cols, rows, 0, &selection{anchor: point{0, 0}, cursor: point{4, 1}})
	if !strings.Contains(got, "\n") {
		t.Errorf("the wrapped row was joined into one line: %q", got)
	}
	if want := strings.Repeat("x", cols) + "\n" + strings.Repeat("x", 5); got != want {
		t.Errorf("wrapped selection:\n got %q\nwant %q", got, want)
	}
}

// ── the scrollback ──────────────────────────────────────────────────────────

// TestSelectionInScrollbackCopiesPastLines: the selection is in viewport
// coordinates, so with the viewport lifted the same row copies the history it is
// showing rather than the live screen behind it.
func TestSelectionInScrollbackCopiesPastLines(t *testing.T) {
	app, tab := selectApp(t)
	cols, rows := app.rightInner()
	for i := range rows * 2 {
		fmt.Fprintf(tab.emu(), "line-%02d\r\n", i)
	}

	app.scrollBy(5)
	if tab.scrollOff != 5 {
		t.Fatalf("scroll offset is %d, want 5", tab.scrollOff)
	}

	live := selectedText(tab.emu(), cols, rows, 0, &selection{anchor: point{0, 0}, cursor: point{7, 0}})
	past := selectedText(tab.emu(), cols, rows, tab.scrollOff, &selection{anchor: point{0, 0}, cursor: point{7, 0}})
	if past == live {
		t.Errorf("scrolled back but copied the live screen: %q", past)
	}
	if !strings.HasPrefix(past, "line-") {
		t.Errorf("copied %q, want a scrollback line", past)
	}
}

// TestScrollClearsSelection: moving the viewport makes the selected rows hold
// different text, so the selection goes rather than being dragged along.
func TestScrollClearsSelection(t *testing.T) {
	app, tab := selectApp(t)
	_, rows := app.rightInner()
	for i := range rows * 2 {
		fmt.Fprintf(tab.emu(), "line-%02d\r\n", i)
	}

	drag(app, 0, 0, 5, 0)
	if tab.sel == nil {
		t.Fatal("the drag left no selection to clear")
	}
	app.scrollBy(3)
	if tab.sel != nil {
		t.Error("the selection survived a scroll")
	}
}

// TestResizeClearsSelection: reflowed history no longer lines up with the old
// coordinates — the same reason the scroll offset is dropped.
func TestResizeClearsSelection(t *testing.T) {
	app, tab := selectApp(t, "hello")
	drag(app, 0, 0, 4, 0)
	if tab.sel == nil {
		t.Fatal("the drag left no selection to clear")
	}
	app.resize(120, 30)
	if tab.sel != nil {
		t.Error("the selection survived a resize")
	}
}

// TestTabSwitchClearsSelection: a selection belongs to the screen it was made on.
func TestTabSwitchClearsSelection(t *testing.T) {
	app, first := selectApp(t, "hello")
	drag(app, 0, 0, 4, 0)
	second := attachTab(app, "web-2")
	app.switchTo(0)

	if first.sel != nil {
		t.Error("the selection survived switching away and back")
	}
	if second.sel != nil {
		t.Error("the new tab started with a selection")
	}
}

// TestKeyToSessionClearsSelection: any key means the user is done reading, which
// is the same judgement that drops the scroll offset.
func TestKeyToSessionClearsSelection(t *testing.T) {
	app, tab := selectApp(t, "hello")
	drag(app, 0, 0, 4, 0)
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if tab.sel != nil {
		t.Error("the selection survived a keypress into the session")
	}
}

// TestNoAutoScrollWhileDragging: a drag that leaves the panel stops at the edge.
// Auto-scrolling would move the viewport, and moving the viewport is exactly what
// clears a selection — the drag would erase itself.
func TestNoAutoScrollWhileDragging(t *testing.T) {
	app, tab := selectApp(t)
	cols, rows := app.rightInner()
	for i := range rows * 2 {
		fmt.Fprintf(tab.emu(), "line-%02d\r\n", i)
	}

	press(app, sessionOriginX+2, sessionOriginY+2)
	motion(app, 0, 0)                        // out over the sidebar and the top margin
	motion(app, app.width+50, app.height+50) // and out past the bottom right

	if tab.scrollOff != 0 {
		t.Errorf("dragging out of the panel scrolled the viewport to %d", tab.scrollOff)
	}
	if tab.sel == nil {
		t.Fatal("the drag was lost on the way out of the panel")
	}
	if got := tab.sel.cursor; got.x >= cols || got.y >= rows || got.x < 0 || got.y < 0 {
		t.Errorf("cursor %+v is outside the %dx%d body", got, cols, rows)
	}
}

// ── the clipboard ───────────────────────────────────────────────────────────

// TestCopyEmitsOSC52Once: the sequence rides out as a prefix on exactly one
// frame. Writing to stdout ourselves would land in the middle of a frame the
// renderer owns, and leaving the prefix up would repeat the copy every redraw.
func TestCopyEmitsOSC52Once(t *testing.T) {
	app, _ := selectApp(t, "copy me")
	cmd := drag(app, 0, 0, 6, 0)

	if app.clip == "" {
		t.Fatal("the release copied nothing")
	}
	if !strings.Contains(app.View(), "\x1b]52;") {
		t.Error("the frame does not carry the clipboard sequence")
	}

	runCmd(app, cmd) // the flush message the runtime would deliver
	if app.clip != "" {
		t.Error("the sequence was not cleared")
	}
	if strings.Contains(app.View(), "\x1b]52;") {
		t.Error("the clipboard sequence is still in the next frame")
	}
}

// TestCopyStatusCountsLines: the status line says what went out.
func TestCopyStatusCountsLines(t *testing.T) {
	app, _ := selectApp(t, "one", "two", "three")
	drag(app, 0, 0, 4, 2)
	if want := "copied 3 lines"; app.status != want {
		t.Errorf("status %q, want %q", app.status, want)
	}
}

// TestClickWithoutDragDoesNotCopy: a click moves focus and nothing else. It also
// must not put an empty selection on the clipboard.
func TestClickWithoutDragDoesNotCopy(t *testing.T) {
	app, tab := selectApp(t, "hello")
	app.focus = focusSidebar

	press(app, sessionOriginX+2, sessionOriginY)
	release(app, sessionOriginX+2, sessionOriginY)

	if app.clip != "" {
		t.Errorf("a plain click copied %q", app.clip)
	}
	if tab.sel != nil {
		t.Error("a plain click left a selection behind")
	}
	// Focus still follows the old rule: the panel takes it only when there is a
	// session (or a lost one waiting for [r]) behind it. This tab has neither,
	// and selecting text must not have changed that.
	if app.focus != focusSidebar {
		t.Errorf("focus moved to %v on a click with no session behind the panel", app.focus)
	}
}

// TestCopyTruncatesAt64KiB: OSC 52 is one unbroken escape sequence, so a huge
// selection is cut — on a rune boundary — and the status line says so.
func TestCopyTruncatesAt64KiB(t *testing.T) {
	long := strings.Repeat("a", clipLimit+100)
	got, cut := truncateClip(long)
	if !cut || len(got) != clipLimit {
		t.Errorf("truncateClip(%d bytes) = %d bytes, cut=%v", len(long), len(got), cut)
	}

	// A multi-byte rune straddling the limit is dropped whole, never halved.
	wide := strings.Repeat("a", clipLimit-1) + "가"
	got, cut = truncateClip(wide)
	if !cut {
		t.Fatal("a selection over the limit was not cut")
	}
	if !strings.HasSuffix(got, "a") || strings.ContainsRune(got, '�') {
		t.Errorf("the cut split a rune: %q", got[len(got)-4:])
	}

	short := "small"
	if got, cut = truncateClip(short); cut || got != short {
		t.Errorf("truncateClip(%q) = %q, %v", short, got, cut)
	}
}

// ── the modal rule and the layout ───────────────────────────────────────────

// TestSelectionBlockedByModal: while a question is on screen nothing underneath
// sees the mouse — the rule the help card and the split view's dialogs follow.
func TestSelectionBlockedByModal(t *testing.T) {
	app, tab := selectApp(t, "hello")
	app.confirm = &confirm{title: "Delete"}
	drag(app, 0, 0, 4, 0)
	if tab.sel != nil || app.clip != "" {
		t.Error("a drag reached the session behind a confirmation")
	}

	app.confirm = nil
	app.openHelp()
	drag(app, 0, 0, 4, 0)
	if tab.sel != nil || app.clip != "" {
		t.Error("a drag reached the session behind the help card")
	}
}

// TestSelectionNeverBreaksLayout: neither the reversed cells nor the zero-width
// clipboard sequence may change the shape of the frame.
func TestSelectionNeverBreaksLayout(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {80, 20}, {140, 40}} {
		app, tab := selectApp(t, "hello world", "second line")
		app.resize(size[0], size[1])
		drag(app, 2, 0, 4, 1)
		if app.clip == "" {
			t.Fatalf("%dx%d: nothing was copied", size[0], size[1])
		}
		// The selection is still up, so this frame carries both.
		tab.sel = &selection{anchor: point{2, 0}, cursor: point{4, 1}}

		lines := strings.Split(app.View(), "\n")
		if len(lines) != size[1] {
			t.Errorf("%dx%d: frame has %d rows, want %d", size[0], size[1], len(lines), size[1])
		}
		for i, line := range lines {
			if w := ansi.StringWidth(line); w != size[0] {
				t.Errorf("%dx%d: row %d is %d wide, want %d", size[0], size[1], i, w, size[0])
			}
		}
	}
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}
