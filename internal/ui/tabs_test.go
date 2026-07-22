package ui

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// attachTab gives the app a tab with a real emulator but no session, which is
// what the view and routing tests need: dialling is the one thing they cannot do.
func attachTab(a *App, name string) *sessionTab {
	cols, rows := a.rightInner()
	slot, ok := a.pool.get(cols, rows)
	if !ok {
		panic("pool exhausted in test")
	}
	a.gen++
	t := &sessionTab{
		id:    name,
		name:  name,
		addr:  "deploy@" + name + ":22",
		srv:   model.Server{ID: name, Name: name, Host: name, User: "deploy", Port: 22},
		gen:   a.gen,
		slot:  slot,
		state: tabLive,
	}
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
	a.rightMode = rightTerminal
	return t
}

func testApp(t *testing.T) *App {
	t.Helper()
	app := New(config.New(t.TempDir()))
	app.resize(100, 24)
	return app
}

// TestTabPoolRecyclesSlots is the goroutine budget: emulators are never closed
// (keyPump explains why), so opening and closing sessions all day must reuse the
// same handful of pumps rather than leaking one per session.
func TestTabPoolRecyclesSlots(t *testing.T) {
	app := testApp(t)

	before := runtime.NumGoroutine()
	for range 20 {
		tab := attachTab(app, "host")
		fmt.Fprint(tab.emu(), "secret from the previous session")
		app.closeTab(app.active)
	}
	// Give the runtime a moment in case a pump is between reads.
	time.Sleep(10 * time.Millisecond)

	if got := runtime.NumGoroutine(); got > before+maxTabs {
		t.Errorf("20 sessions left %d goroutines, want at most %d", got, before+maxTabs)
	}
	if app.pool.live > maxTabs {
		t.Errorf("pool grew to %d slots, want at most %d", app.pool.live, maxTabs)
	}

	// A recycled slot must not hand the next session the last one's screen.
	tab := attachTab(app, "host")
	cols, rows := app.rightInner()
	if got := stripANSI(renderEmulator(tab.emu(), cols, rows, false)); strings.Contains(got, "secret") {
		t.Error("a recycled emulator still holds the previous session's output")
	}
}

// TestTabCeilingIsRefused checks the ceiling is the pool's, and that it is a
// message rather than a silent no-op.
func TestTabCeilingIsRefused(t *testing.T) {
	app := testApp(t)
	srv := model.Server{ID: "1", Name: "web", Host: "h", User: "u", Port: 22, Auth: model.AuthPassword, Password: "p"}
	app.servers = []model.Server{srv}
	app.sidebar.SetServers(app.servers)

	for i := range maxTabs {
		if cmd := app.openTab(srv, true); cmd == nil {
			t.Fatalf("tab %d was refused too early", i+1)
		}
	}
	if cmd := app.openTab(srv, true); cmd != nil {
		t.Error("the ninth tab should not dial")
	}
	if len(app.tabs) != maxTabs {
		t.Errorf("open tabs: %d, want %d", len(app.tabs), maxTabs)
	}
	if !strings.Contains(app.status, "too many sessions") {
		t.Errorf("status should say why, got %q", app.status)
	}

	// The list marks servers that already have a session: enter switches to
	// those rather than dialling again.
	if got := stripANSI(app.sidebar.View()); !strings.Contains(got, "● web") {
		t.Errorf("sidebar should mark the open server, got:\n%s", got)
	}

	// Closing one frees a slot, and the freed slot is reused rather than new.
	live := app.pool.live
	app.closeTab(0)
	if cmd := app.openTab(srv, true); cmd == nil {
		t.Fatal("a tab should be available after closing one")
	}
	if app.pool.live != live {
		t.Errorf("pool grew to %d slots instead of reusing the freed one (%d)", app.pool.live, live)
	}
}

// TestBackgroundTabKeepsReceivingOutput is the point of the whole version: a
// session you are not looking at is still running, and its output lands in its
// own emulator rather than on the screen.
func TestBackgroundTabKeepsReceivingOutput(t *testing.T) {
	app := testApp(t)
	a := attachTab(app, "alpha")
	b := attachTab(app, "beta")
	app.switchTo(0) // looking at alpha

	var m tea.Model = app
	m, _ = m.Update(outputMsg{gen: b.gen, data: []byte("background work\r\n")})

	cols, rows := app.rightInner()
	if got := stripANSI(renderEmulator(b.emu(), cols, rows, false)); !strings.Contains(got, "background work") {
		t.Error("output for a background tab did not reach its emulator")
	}
	if got := stripANSI(renderEmulator(a.emu(), cols, rows, false)); strings.Contains(got, "background work") {
		t.Error("output for a background tab leaked into the visible one")
	}
	if !b.activity {
		t.Error("a background tab with new output should be marked")
	}
	if !strings.Contains(stripANSI(app.rightHeader(cols)), "•") {
		t.Error("the tab strip should show the activity marker")
	}

	// Switching to it clears the marker.
	app.switchTo(1)
	if b.activity {
		t.Error("looking at the tab should clear its activity marker")
	}

	// A message from a closed tab belongs to nobody and must be dropped.
	gen := a.gen
	app.closeTab(0)
	if _, ok := app.tabByGen(gen); ok {
		t.Fatal("a closed tab still answers to its generation")
	}
	m.Update(outputMsg{gen: gen, data: []byte("ghost")})
	for _, tab := range app.tabs {
		if got := stripANSI(renderEmulator(tab.emu(), cols, rows, false)); strings.Contains(got, "ghost") {
			t.Error("output from a closed session reached a live emulator")
		}
	}
}

// TestAltKeysSwitchTabs pins the keymap decision: alt switches, ctrl+b still
// means what it always did, and a dialog outranks both.
func TestAltKeysSwitchTabs(t *testing.T) {
	app := testApp(t)
	attachTab(app, "alpha")
	attachTab(app, "beta")
	attachTab(app, "gamma")
	app.focus = focusSession

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if app.active != 0 {
		t.Errorf("alt+1: active tab %d, want 0", app.active)
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	if app.active != 1 {
		t.Errorf("alt+right: active tab %d, want 1", app.active)
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	app.handleKey(tea.KeyMsg{Type: tea.KeyLeft, Alt: true})
	if app.active != 2 {
		t.Errorf("alt+left should wrap around, active tab %d, want 2", app.active)
	}

	// ctrl+b keeps its v1 meaning: leave the session, do not close it.
	app.focus = focusSession
	app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlB})
	if app.focus != focusSidebar || len(app.tabs) != 3 {
		t.Errorf("ctrl+b: focus=%v tabs=%d, want sidebar and 3 tabs", app.focus, len(app.tabs))
	}

	// A confirmation owns the keyboard, tab switching included.
	app.focus = focusSession
	app.active = 0
	app.confirm = &confirm{title: "Delete", lines: []string{"?"}, accept: "[enter] delete"}
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3"), Alt: true})
	if app.active != 0 {
		t.Errorf("alt+3 leaked past a confirmation, active tab %d", app.active)
	}
	app.confirm = nil

	// alt+w closes the visible tab and leaves the rest alone.
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w"), Alt: true})
	if len(app.tabs) != 2 || app.tabs[0].name != "beta" {
		t.Errorf("alt+w: %d tabs left, first is %q", len(app.tabs), app.tabs[0].name)
	}

	// Closing the last one drops back to the empty panel.
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w"), Alt: true})
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("w"), Alt: true})
	if len(app.tabs) != 0 || app.active != -1 || app.rightMode != rightEmpty {
		t.Errorf("closing every tab: tabs=%d active=%d mode=%v", len(app.tabs), app.active, app.rightMode)
	}
}

// TestResizeUpdatesEveryTab covers the tab version of v0's rule that layout,
// emulator and remote PTY move together: a background tab that kept the old
// geometry would be drawn wrong the moment you switch to it.
func TestResizeUpdatesEveryTab(t *testing.T) {
	app := testApp(t)
	a := attachTab(app, "alpha")
	b := attachTab(app, "beta")
	a.scrollOff, b.scrollOff = 5, 5

	app.resize(140, 40)
	cols, rows := app.rightInner()
	for _, tab := range []*sessionTab{a, b} {
		got := strings.Split(renderEmulator(tab.emu(), cols, rows, false), "\n")
		if len(got) != rows {
			t.Errorf("%s: emulator has %d rows, want %d", tab.name, len(got), rows)
		}
		if ansi.StringWidth(got[0]) != cols {
			t.Errorf("%s: emulator is %d columns, want %d", tab.name, ansi.StringWidth(got[0]), cols)
		}
		if tab.scrollOff != 0 {
			t.Errorf("%s: resize should reset the scroll offset, got %d", tab.name, tab.scrollOff)
		}
	}
}

// TestLostSessionReconnects covers the split that keeps an "exit" from turning
// into an endless retry loop: only a connection that died under us comes back.
func TestLostSessionReconnects(t *testing.T) {
	app := testApp(t)
	tab := attachTab(app, "alpha")

	var m tea.Model = app
	_, cmd := m.Update(sessionEndedMsg{gen: tab.gen, err: fmt.Errorf("%w: no reply", sshpkg.ErrConnectionLost)})
	if cmd == nil {
		t.Fatal("a lost connection should schedule a reconnect")
	}
	if tab.state != tabLost || tab.attempt != 1 {
		t.Fatalf("state=%v attempt=%d, want lost/1", tab.state, tab.attempt)
	}
	if len(app.tabs) != 1 {
		t.Fatal("a lost session must keep its tab")
	}

	// The last screen stays readable while the reconnect is pending, and the
	// status line says what is happening.
	cols, rows := app.rightInner()
	fmt.Fprint(tab.emu(), "half-written command")
	if got := stripANSI(app.rightBody(cols, rows)); !strings.Contains(got, "half-written command") {
		t.Error("a lost tab should keep showing its last screen")
	}
	if got := stripANSI(app.statusLine()); !strings.Contains(got, "reconnecting in") {
		t.Errorf("status line: %q", got)
	}

	// r skips the backoff, and takes a fresh generation so the pending tick is
	// dropped when it lands.
	old := tab.gen
	app.focus = focusSession
	if cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")}); cmd == nil {
		t.Fatal("r should dial immediately")
	}
	if tab.state != tabReconnecting || tab.gen == old {
		t.Errorf("manual retry: state=%v gen=%d (was %d)", tab.state, tab.gen, old)
	}
	m.Update(reconnectMsg{gen: old})
	if tab.state != tabReconnecting {
		t.Error("a superseded backoff tick should be dropped")
	}

	// A clean exit closes the tab instead.
	m.Update(sessionEndedMsg{gen: tab.gen})
	if len(app.tabs) != 0 {
		t.Errorf("a clean exit should close the tab, %d left", len(app.tabs))
	}
}

// TestUnattendedHostKeyStopsRetrying: an automatic retry must never be the thing
// that approves a new host key, so that failure parks the tab instead of looping.
func TestUnattendedHostKeyStopsRetrying(t *testing.T) {
	app := testApp(t)
	tab := attachTab(app, "alpha")
	tab.attempt = 2
	tab.state = tabReconnecting

	var m tea.Model = app
	_, cmd := m.Update(connectFailedMsg{gen: tab.gen, err: fmt.Errorf("%w: not in known_hosts", sshpkg.ErrHostKeyUnknown)})
	if cmd != nil {
		t.Error("an unknown host key must not schedule another unattended attempt")
	}
	if tab.state != tabLost || !tab.until.IsZero() {
		t.Errorf("state=%v until=%v, want a parked tab", tab.state, tab.until)
	}
	if len(app.tabs) != 1 {
		t.Fatal("the tab should stay so its screen is still readable")
	}
	if got := stripANSI(app.statusLine()); !strings.Contains(got, "[r] reconnect") {
		t.Errorf("status line should offer a manual retry, got %q", got)
	}

	// A failed reconnect for any other reason keeps backing off.
	tab.state, tab.attempt = tabReconnecting, 2
	if _, cmd := m.Update(connectFailedMsg{gen: tab.gen, err: fmt.Errorf("%w: refused", sshpkg.ErrUnreachable)}); cmd == nil {
		t.Error("an unreachable host should keep retrying")
	}
	if tab.attempt != 3 {
		t.Errorf("attempt=%d, want 3", tab.attempt)
	}
}

func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %s, want %s", i+1, got, w)
		}
	}
}

// TestLayoutAlignmentWithTabs is TestLayoutAlignment's invariant under a full
// tab strip: the strip lives in the title row it already had, so the panels are
// exactly as tall as they were before tabs existed.
func TestLayoutAlignmentWithTabs(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {80, 30}, {200, 60}, {40, 10}} {
		width, height := size[0], size[1]
		app := New(config.New(t.TempDir()))
		app.resize(width, height)

		bare := app.panelHeight()
		_, bareRows := app.rightInner()
		for i := range maxTabs {
			attachTab(app, fmt.Sprintf("a-rather-long-hostname-%d", i))
		}
		app.tabs[3].activity = true
		app.tabs[5].state = tabLost

		lines := strings.Split(app.View(), "\n")
		if len(lines) != height {
			t.Fatalf("%dx%d: %d rows, want %d", width, height, len(lines), height)
		}
		for i, l := range lines {
			if w := ansi.StringWidth(l); w != width {
				t.Errorf("%dx%d: row %d is %d columns wide, want %d", width, height, i, w, width)
			}
		}
		if got := app.panelHeight(); got != bare {
			t.Errorf("%dx%d: tabs changed the panel height: %d, want %d", width, height, got, bare)
		}
		if _, rows := app.rightInner(); rows != bareRows {
			t.Errorf("%dx%d: tabs changed the body height: %d, want %d", width, height, rows, bareRows)
		}
		if strings.Count(stripANSI(lines[topMargin]), "╭") != 2 {
			t.Errorf("%dx%d: panels no longer open on the same row", width, height)
		}
	}
}

// TestSingleTabHeaderIsUnchanged: one session must look exactly like it did
// before there were tabs — no strip, no markers, name and address as before.
func TestSingleTabHeaderIsUnchanged(t *testing.T) {
	app := testApp(t)
	tab := attachTab(app, "prod-web")
	cols, _ := app.rightInner()

	header := stripANSI(app.rightHeader(cols))
	if !strings.Contains(header, "prod-web") || !strings.Contains(header, tab.addr) {
		t.Errorf("single tab header: %q", header)
	}
	for _, marker := range []string{"‹", "›", "•"} {
		if strings.Contains(header, marker) {
			t.Errorf("single tab header should carry no strip markers, got %q", header)
		}
	}

	// A second session turns the same row into the strip, naming both.
	attachTab(app, "db-1")
	header = stripANSI(app.rightHeader(cols))
	for _, want := range []string{"prod-web", "db-1"} {
		if !strings.Contains(header, want) {
			t.Errorf("tab strip is missing %q: %q", want, header)
		}
	}
	if lines := strings.Split(app.rightHeader(cols), "\n"); len(lines) != rightHeaderRows {
		t.Errorf("the header must stay %d rows, got %d", rightHeaderRows, len(lines))
	}
}
