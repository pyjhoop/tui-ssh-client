package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// smoke drives the root model without a terminal: layout, focus switching and
// the form flow must not panic and must render at the requested size.
func TestSmoke(t *testing.T) {
	dir := t.TempDir()
	app := New(config.New(dir))

	var m tea.Model = app
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if v := m.View(); strings.Count(v, "\n") == 0 {
		t.Fatal("empty view after resize")
	}

	// + Connect → form
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if app.rightMode != rightForm || app.focus != focusForm {
		t.Fatalf("expected form mode, got rightMode=%v focus=%v", app.rightMode, app.focus)
	}

	// Fill in host / port / user, tabbing through the fields.
	typeText := func(s string) {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → Group
	typeText("prod")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → Host
	typeText("example.com")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → Port
	typeText("2222")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → User
	typeText("deploy")
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → Auth
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // → Password
	typeText("hunter2")

	srv, keyBody, err := app.form.Server()
	if err != nil {
		t.Fatalf("form.Server: %v", err)
	}
	if keyBody != "" {
		t.Errorf("no key was pasted, got body %q", keyBody)
	}
	want := model.Server{Host: "example.com", Port: 2222, User: "deploy", Auth: model.AuthPassword, Password: "hunter2", Group: "prod"}
	if srv != want {
		t.Fatalf("form.Server: got %+v, want %+v", srv, want)
	}

	// Save it and let the resulting command run.
	cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("save produced no command")
	}
	m, _ = m.Update(cmd())
	if len(app.servers) != 1 {
		t.Fatalf("want 1 saved server, got %d", len(app.servers))
	}
	if !strings.Contains(app.warning, "plaintext") {
		t.Errorf("expected the plaintext warning, got %q", app.warning)
	}

	// The saved server shows up in the sidebar, and the view still renders.
	if !strings.Contains(m.View(), "deploy@example.com") {
		t.Error("saved server missing from the sidebar")
	}

	// Terminal rendering: a bare emulator must produce exactly rows lines of
	// cols columns.
	cols, rows := app.rightInner()
	emu := newEmulator(cols, rows)
	_, _ = emu.Write([]byte("hello\r\n\x1b[31mred\x1b[0m"))
	out := renderEmulator(emu, cols, rows, true)
	lines := strings.Split(out, "\n")
	if len(lines) != rows {
		t.Fatalf("rendered %d lines, want %d", len(lines), rows)
	}
	if !strings.Contains(lines[0], "hello") {
		t.Errorf("first line missing output: %q", lines[0])
	}
}

// TestLayoutAlignment pins the geometry: one margin row, two panels whose
// borders start and end on the same rows, and a status line, all filling the
// terminal exactly.
func TestLayoutAlignment(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {80, 30}, {200, 60}, {40, 10}} {
		width, height := size[0], size[1]

		app := New(config.New(t.TempDir()))
		app.servers = []model.Server{{ID: "1", Name: "alpha", Host: "a", User: "u", Port: 22}}
		app.sidebar.SetServers(app.servers)
		app.resize(width, height)

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
		for i := range topMargin {
			if strings.TrimSpace(lines[i]) != "" {
				t.Errorf("%dx%d: row %d should be the top margin, got %q", width, height, i, lines[i])
			}
		}

		// The two panels must open and close on the same rows.
		top, bottom := lines[topMargin], lines[height-statusRows-1]
		if strings.Count(stripANSI(top), "╭") != 2 {
			t.Errorf("%dx%d: row %d is not both panel tops: %q", width, height, topMargin, stripANSI(top))
		}
		if strings.Count(stripANSI(bottom), "╰") != 2 {
			t.Errorf("%dx%d: row %d is not both panel bottoms: %q", width, height, height-statusRows-1, stripANSI(bottom))
		}
	}
}

// TestRightPanelHeaderNamesTheSession checks the panel always says what you
// are looking at, and that the title bar costs exactly rightHeaderRows.
func TestRightPanelHeaderNamesTheSession(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.resize(100, 24)

	if got := stripANSI(strings.Split(app.View(), "\n")[topMargin+1]); !strings.Contains(got, "ssh-client") {
		t.Errorf("idle header: got %q, want it to mention ssh-client", got)
	}

	app.rightMode = rightForm
	if got := stripANSI(strings.Split(app.View(), "\n")[topMargin+1]); !strings.Contains(got, "New connection") {
		t.Errorf("form header: got %q", got)
	}

	tab := attachTab(app, "prod-web")
	tab.addr = "deploy@10.0.0.1:22"

	header := stripANSI(strings.Split(app.View(), "\n")[topMargin+1])
	if !strings.Contains(header, "prod-web") || !strings.Contains(header, "deploy@10.0.0.1:22") {
		t.Errorf("session header: got %q, want the name and address", header)
	}

	// The emulator must be sized to the body, not the whole panel, or the
	// remote PTY and the visible grid disagree.
	_, rows := app.rightInner()
	if want := app.panelHeight() - rightHeaderRows; rows != want {
		t.Errorf("terminal body height: got %d, want %d", rows, want)
	}
}

// TestSidebarRowGeometry pins the constants rowToIndex relies on against what
// the list actually renders, so a delegate change cannot silently break
// click-to-select.
func TestSidebarRowGeometry(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.servers = []model.Server{
		{ID: "1", Name: "alpha", Host: "a", User: "u", Port: 22},
		{ID: "2", Name: "beta", Host: "b", User: "u", Port: 22},
	}
	app.sidebar.SetServers(app.servers)
	app.resize(100, 24)

	lines := strings.Split(app.View(), "\n")
	for wantIdx, label := range []string{"+ Connect", "alpha", "beta"} {
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

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			esc = true
		case esc && (r == 'm' || r == 'K' || r == 'H'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TestSendKeyReachesTheInputPipe covers the path the session drain goroutine
// relies on: a Bubble Tea key becomes ANSI bytes on the emulator's input pipe,
// which app.go copies straight into the remote shell's stdin.
func TestSendKeyReachesTheInputPipe(t *testing.T) {
	emu := newEmulator(80, 24)
	defer emu.Close()

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		var seen strings.Builder
		for {
			n, err := emu.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
				if strings.Contains(seen.String(), "\x1b[A") {
					got <- seen.String()
					return
				}
			}
			if err != nil {
				got <- seen.String()
				return
			}
		}
	}()

	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("a")},
		{Type: tea.KeyEnter},
		{Type: tea.KeyUp},
	} {
		key, ok := keyToVT(msg)
		if !ok {
			t.Fatalf("keyToVT(%v) reported no mapping", msg)
		}
		emu.SendKey(key)
	}

	select {
	case out := <-got:
		if want := "a\r\x1b[A"; out != want {
			t.Errorf("input pipe: got %q, want %q", out, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading the emulator input pipe")
	}
}

// TestWriteDoesNotBlockOnTerminalQueries guards the deadlock that a plain
// io.Copy drain would reintroduce: remote output containing a cursor-position
// request makes the emulator write its answer to the input pipe from inside
// emu.Write, which runs on the UI goroutine.
func TestWriteDoesNotBlockOnTerminalQueries(t *testing.T) {
	emu := newEmulator(80, 24)

	replies := make(chan []byte, 4)
	var pump keyPump
	pump.attach(writerFunc(func(p []byte) (int, error) {
		out := make([]byte, len(p))
		copy(out, p)
		replies <- out
		return len(p), nil
	}))
	// The pump outlives the test on purpose: closing the emulator to stop it is
	// exactly the race this design avoids.
	go pump.run(emu)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// ESC[6n is a device status report request; bash and vim send it often.
		_, _ = emu.Write([]byte("prompt$ \x1b[6n"))
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("emu.Write blocked on a terminal query — the input pipe is not being drained")
	}

	select {
	case r := <-replies:
		if !strings.HasPrefix(string(r), "\x1b[") || !strings.HasSuffix(string(r), "R") {
			t.Errorf("want a cursor position report, got %q", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no reply to the cursor position request")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestKeyToVTCoversControlAndSpecialKeys(t *testing.T) {
	cases := []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("a")},
		{Type: tea.KeyEnter},
		{Type: tea.KeyTab},
		{Type: tea.KeyEsc},
		{Type: tea.KeyBackspace},
		{Type: tea.KeyUp},
		{Type: tea.KeyF5},
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyCtrlD},
		{Type: tea.KeySpace},
	}
	for _, c := range cases {
		if _, ok := keyToVT(c); !ok {
			t.Errorf("keyToVT(%v) reported no mapping", c)
		}
	}
}

// TestScrollbackRendersHistory checks the scrolled viewport: the top rows come
// from the vt scrollback, the rest from the live screen, and the block is still
// exactly rows × cols.
func TestScrollbackRendersHistory(t *testing.T) {
	const cols, rows = 40, 10
	emu := newEmulator(cols, rows)

	for i := range 200 {
		fmt.Fprintf(emu, "line %d\r\n", i)
	}
	if emu.ScrollbackLen() == 0 {
		t.Fatal("nothing was pushed into the scrollback")
	}

	const offset = 4
	out := renderScrolled(emu, cols, rows, offset, false)
	lines := strings.Split(out, "\n")
	if len(lines) != rows {
		t.Fatalf("rendered %d lines, want %d", len(lines), rows)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != cols {
			t.Errorf("row %d is %d columns wide, want %d", i, w, cols)
		}
	}

	// The first visible row is the line offset positions back from the top of
	// the live screen.
	sb := emu.Scrollback()
	wantFirst := strings.TrimSpace(stripANSI(sb.Line(sb.Len() - offset).Render()))
	if got := strings.TrimSpace(stripANSI(lines[0])); got != wantFirst {
		t.Errorf("first row: got %q, want the scrollback line %q", got, wantFirst)
	}

	// And row `offset` is what used to be the top of the screen.
	screenTop := strings.TrimSpace(stripANSI(strings.Split(emu.Render(), "\n")[0]))
	if got := strings.TrimSpace(stripANSI(lines[offset])); got != screenTop {
		t.Errorf("row %d: got %q, want the screen's first row %q", offset, got, screenTop)
	}

	// offset 0 must be identical to the unscrolled render.
	if renderScrolled(emu, cols, rows, 0, false) != renderEmulator(emu, cols, rows, false) {
		t.Error("offset 0 should take the plain render path")
	}
}

// TestScrollOffsetClampsAndResets covers the state rules around scrolling: it
// never runs past the buffer, any key drops back to the bottom, and a resize
// clears it because the reflowed history no longer matches.
func TestScrollOffsetClampsAndResets(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.resize(100, 24)
	cols, rows := app.rightInner()
	tab := attachTab(app, "prod-web")
	app.focus = focusSession

	for i := range 100 {
		fmt.Fprintf(tab.emu(), "line %d\r\n", i)
	}

	app.scrollBy(1000)
	if app.scrollOffset() != tab.emu().ScrollbackLen() {
		t.Errorf("scroll offset %d should clamp to the scrollback length %d", app.scrollOffset(), tab.emu().ScrollbackLen())
	}
	app.scrollBy(-10000)
	if app.scrollOffset() != 0 {
		t.Errorf("scroll offset %d should clamp to 0", app.scrollOffset())
	}

	// The title bar has to say we are looking at the past.
	app.scrollBy(5)
	if got := stripANSI(app.rightHeader(cols)); !strings.Contains(got, "SCROLL −5") {
		t.Errorf("header should announce the scroll offset, got %q", got)
	}
	_ = rows

	// shift+up scrolls without reaching the session; a plain key returns to the
	// bottom.
	before := app.scrollOffset()
	app.handleKey(tea.KeyMsg{Type: tea.KeyShiftUp})
	if app.scrollOffset() != before+scrollStep {
		t.Errorf("shift+up: offset %d, want %d", app.scrollOffset(), before+scrollStep)
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if app.scrollOffset() != 0 {
		t.Errorf("any other key should jump back to the bottom, offset is %d", app.scrollOffset())
	}

	app.scrollBy(5)
	app.resize(120, 30)
	if app.scrollOffset() != 0 {
		t.Errorf("resize should reset the scroll offset, got %d", app.scrollOffset())
	}
}

// TestConfirmPanelSwallowsKeys is the containment property: while a confirm is
// up, nothing reaches the session or the list behind it.
func TestConfirmPanelSwallowsKeys(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.resize(100, 24)
	attachTab(app, "prod-web")
	app.focus = focusSession

	answered := 0
	app.confirm = &confirm{
		title:  "Delete connection",
		lines:  []string{"Delete alpha?"},
		accept: "[enter] delete",
		onYes:  func() tea.Msg { answered++; return nil },
	}

	// Unrelated keys are dropped, not forwarded.
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune("x")},
		{Type: tea.KeyUp},
		{Type: tea.KeyCtrlB},
	} {
		if cmd := app.handleKey(k); cmd != nil {
			t.Errorf("%v produced a command while a confirm was up", k)
		}
		if app.confirm == nil {
			t.Fatalf("%v dismissed the confirm", k)
		}
		if app.focus != focusSession {
			t.Errorf("%v changed focus to %v", k, app.focus)
		}
	}

	// The panel body is the confirmation, not the terminal.
	cols, rows := app.rightInner()
	if got := stripANSI(app.rightBody(cols, rows)); !strings.Contains(got, "Delete alpha?") {
		t.Errorf("confirm should replace the panel body, got %q", got)
	}

	if cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd == nil {
		t.Fatal("enter should run onYes")
	} else {
		cmd()
	}
	if answered != 1 {
		t.Errorf("onYes ran %d times, want 1", answered)
	}
	if app.confirm != nil {
		t.Error("answering should dismiss the confirm")
	}
}

// TestEditFormPrefills round-trips a server through the edit form.
func TestEditFormPrefills(t *testing.T) {
	srv := model.Server{
		ID: "abc", Name: "prod-web", Host: "10.0.0.1", Port: 2222,
		User: "deploy", Auth: model.AuthPassword, Password: "hunter2",
	}

	f := newFormFor(srv, 60, 20)
	if f.editingID != srv.ID {
		t.Errorf("editingID: got %q, want %q", f.editingID, srv.ID)
	}

	got, keyBody, err := f.Server()
	if err != nil {
		t.Fatalf("form.Server: %v", err)
	}
	if keyBody != "" {
		t.Errorf("nothing was pasted, got key body %q", keyBody)
	}
	if got != srv {
		t.Errorf("round trip: got %+v, want %+v", got, srv)
	}

	// Key auth keeps the stored path and leaves the paste area empty, so saving
	// without pasting keeps the existing keys/<id>.pem.
	keySrv := model.Server{ID: "def", Host: "h", Port: 22, User: "u", Auth: model.AuthKey, KeyPath: "/tmp/keys/def.pem"}
	kf := newFormFor(keySrv, 60, 20)
	gotKey, body, err := kf.Server()
	if err != nil {
		t.Fatalf("form.Server: %v", err)
	}
	if body != "" {
		t.Errorf("the key body must start empty, got %q", body)
	}
	if gotKey != keySrv {
		t.Errorf("round trip: got %+v, want %+v", gotKey, keySrv)
	}
}

// TestEditKeyOpensThePrefilledForm walks the sidebar binding end to end.
func TestEditKeyOpensThePrefilledForm(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.servers = []model.Server{{ID: "1", Name: "alpha", Host: "a", Port: 22, User: "u", Auth: model.AuthPassword, Password: "p"}}
	app.sidebar.SetServers(app.servers)
	app.resize(100, 24)

	// Row 0 is "+ Connect", which has nothing to edit.
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if app.rightMode == rightForm {
		t.Fatal("e on “+ Connect” should do nothing")
	}

	app.sidebar.list.Select(1)
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if app.rightMode != rightForm || app.focus != focusForm {
		t.Fatalf("e should open the form, got rightMode=%v focus=%v", app.rightMode, app.focus)
	}
	if app.form.editingID != "1" {
		t.Errorf("editingID: got %q, want %q", app.form.editingID, "1")
	}
	if got := stripANSI(app.rightHeader(80)); !strings.Contains(got, "Edit connection") {
		t.Errorf("header: got %q, want it to say Edit connection", got)
	}
}

// TestDeleteAsksFirst: d must not delete on its own any more.
func TestDeleteAsksFirst(t *testing.T) {
	store := config.New(t.TempDir())
	saved, err := store.Add(model.Server{Name: "alpha", Host: "a", Port: 22, User: "u", Auth: model.AuthPassword, Password: "p"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	app := New(store)
	app.servers = []model.Server{saved}
	app.sidebar.SetServers(app.servers)
	app.resize(100, 24)
	app.sidebar.list.Select(1)

	if cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")}); cmd != nil {
		t.Fatal("d should only raise a confirmation, not act")
	}
	if app.confirm == nil {
		t.Fatal("d should raise a confirmation")
	}

	// Cancelling leaves the entry alone.
	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if left, _ := store.Load(); len(left) != 1 {
		t.Fatalf("cancelling deleted the entry anyway, %d left", len(left))
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("confirming should produce the removal command")
	}
	cmd()
	if left, _ := store.Load(); len(left) != 0 {
		t.Fatalf("confirming did not delete the entry, %d left", len(left))
	}
}

// TestErrorCardOffersActions checks the typed-error path all the way to the
// panel: the advice comes from errors.Is, never from message text.
func TestErrorCardOffersActions(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.resize(100, 24)
	srv := model.Server{ID: "1", Name: "prod-web", Host: "10.0.0.1", Port: 22, User: "deploy", Auth: model.AuthPassword, Password: "p"}
	app.servers = []model.Server{srv}
	app.sidebar.SetServers(app.servers)
	app.lastAttempt = srv
	tab := attachTab(app, srv.Title())
	tab.state = tabConnecting

	var m tea.Model = app
	m, _ = m.Update(connectFailedMsg{gen: tab.gen, err: fmt.Errorf("%w: whatever the server said", sshpkg.ErrAuth)})
	if app.rightMode != rightError {
		t.Fatalf("a failed connect should show the error card, rightMode=%v", app.rightMode)
	}

	cols, rows := app.rightInner()
	body := stripANSI(app.rightBody(cols, rows))
	for _, want := range []string{"authentication failed", "deploy@10.0.0.1:22", "[e] edit connection"} {
		if !strings.Contains(body, want) {
			t.Errorf("error card is missing %q; body is:\n%s", want, body)
		}
	}
	if got := stripANSI(app.rightHeader(cols)); !strings.Contains(got, "Connection failed") {
		t.Errorf("header: got %q", got)
	}

	// A mismatched host key gets no retry: retrying is not the fix.
	_, _, actions := errorAdvice(fmt.Errorf("%w: x", sshpkg.ErrHostKeyMismatch), srv)
	for _, a := range actions {
		if strings.Contains(a, "[r]") {
			t.Errorf("host key mismatch must not offer retry, got %v", actions)
		}
	}

	// e opens the edit form for the server that failed.
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if app.rightMode != rightForm || app.form.editingID != "1" {
		t.Fatalf("e should edit the failed server, rightMode=%v editingID=%q", app.rightMode, app.form.editingID)
	}

	// esc dismisses.
	app.rightMode = rightError
	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if app.rightMode != rightEmpty {
		t.Errorf("esc should dismiss the card, rightMode=%v", app.rightMode)
	}
}

// TestLayoutAlignmentWithPanels re-runs the geometry check in the two states v1
// adds, since both replace the right panel's body.
func TestLayoutAlignmentWithPanels(t *testing.T) {
	states := map[string]func(*App){
		"confirm": func(a *App) {
			a.confirm = &confirm{
				title:  "Unknown host",
				lines:  []string{"10.0.0.1:22", "ED25519  SHA256:abcdefghijklmnopqrstuvwxyz0123456789+/AAA"},
				warn:   "Verify this fingerprint against the server itself.",
				accept: "[enter] trust and connect",
			}
		},
		"error card": func(a *App) {
			a.rightMode = rightError
			a.lastAttempt = model.Server{ID: "1", Name: "prod-web", Host: "10.0.0.1", Port: 22, User: "deploy"}
			a.failErr = fmt.Errorf("%w: nope", sshpkg.ErrHostKeyMismatch)
		},
	}

	for name, setup := range states {
		for _, size := range [][2]int{{100, 24}, {80, 30}, {200, 60}, {40, 10}} {
			width, height := size[0], size[1]

			app := New(config.New(t.TempDir()))
			app.servers = []model.Server{{ID: "1", Name: "alpha", Host: "a", User: "u", Port: 22}}
			app.sidebar.SetServers(app.servers)
			app.resize(width, height)
			setup(app)

			lines := strings.Split(app.View(), "\n")
			if len(lines) != height {
				t.Errorf("%s %dx%d: rendered %d rows, want %d", name, width, height, len(lines), height)
				continue
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != width {
					t.Errorf("%s %dx%d: row %d is %d columns wide, want %d", name, width, height, i, w, width)
				}
			}
			if bottom := lines[height-statusRows-1]; strings.Count(stripANSI(bottom), "╰") != 2 {
				t.Errorf("%s %dx%d: panels do not close on the same row: %q", name, width, height, stripANSI(bottom))
			}
		}
	}
}

// TestLayoutAlignmentWithGroupsAndImport extends the alignment invariant over
// everything v5 adds: group headers, a live filter, and the import preview. The
// vertical budget must be unchanged — none of these may cost the panels a row.
func TestLayoutAlignmentWithGroupsAndImport(t *testing.T) {
	longName := strings.Repeat("very-long-server-name-", 4)

	states := map[string]func(*App){
		"groups": func(a *App) {},
		"collapsed group": func(a *App) {
			a.sidebar.SetCollapsed([]string{"prod"})
		},
		"filtering": func(a *App) {
			a.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
			a.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("prod")})
		},
		"import": func(a *App) {
			a.prevRight = a.rightMode
			a.rightMode = rightImport
			a.focus = focusImport
			a.Update(sshConfigParsedMsg{path: "/tmp/ssh_config", entries: []config.SSHConfigEntry{
				{Alias: longName, Host: "10.0.0.1", User: "deploy", Port: 22, Identity: "/home/me/.ssh/id_ed25519"},
				{Alias: "*", Skip: true, Reason: "wildcard pattern"},
			}})
		},
		"import while parsing": func(a *App) {
			a.rightMode = rightImport
			a.focus = focusImport
			a.importing = &importer{path: "/tmp/ssh_config"}
		},
	}

	for name, setup := range states {
		for _, size := range [][2]int{{100, 24}, {80, 30}, {200, 60}, {40, 10}} {
			width, height := size[0], size[1]

			app := New(config.New(t.TempDir()))
			app.servers = []model.Server{
				{ID: "1", Name: "laptop", Host: "10.0.0.1", User: "me", Port: 22},
				{ID: "2", Name: longName, Host: "10.0.1.1", User: "deploy", Port: 22, Group: "prod"},
				{ID: "3", Name: "web-2", Host: "10.0.1.2", User: "deploy", Port: 22, Group: "prod"},
			}
			app.sidebar.SetServers(app.servers)
			app.resize(width, height)
			setup(app)

			lines := strings.Split(app.View(), "\n")
			if len(lines) != height {
				t.Errorf("%s %dx%d: rendered %d rows, want %d", name, width, height, len(lines), height)
				continue
			}
			for i, l := range lines {
				if w := ansi.StringWidth(l); w != width {
					t.Errorf("%s %dx%d: row %d is %d columns wide, want %d", name, width, height, i, w, width)
				}
			}
			if bottom := lines[height-statusRows-1]; strings.Count(stripANSI(bottom), "╰") != 2 {
				t.Errorf("%s %dx%d: panels do not close on the same row: %q", name, width, height, stripANSI(bottom))
			}
		}
	}
}

// TestTabsSurviveImportAndFilter: the list is a way to pick a server, not a
// thing sessions depend on. Filtering it or importing into it must not disturb
// a single tab.
func TestTabsSurviveImportAndFilter(t *testing.T) {
	app := New(config.New(t.TempDir()))
	app.servers = []model.Server{
		{ID: "1", Name: "a", Host: "10.0.0.1", User: "u", Port: 22},
		{ID: "2", Name: "b", Host: "10.0.0.2", User: "u", Port: 22, Group: "prod"},
	}
	app.sidebar.SetServers(app.servers)
	app.resize(120, 40)

	app.tabs = []*sessionTab{{name: "a", gen: 1}, {name: "b", gen: 2}}
	app.active = 1

	// Filter, then leave it.
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})

	// Fold a group.
	app.sidebar.list.Select(2)
	app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})

	// Open and cancel the import preview.
	app.prevRight = app.rightMode
	app.rightMode = rightImport
	app.focus = focusImport
	app.Update(sshConfigParsedMsg{path: "/tmp/x", entries: []config.SSHConfigEntry{
		{Alias: "c", Host: "10.0.0.3", User: "u", Port: 22},
	}})
	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})

	if len(app.tabs) != 2 {
		t.Fatalf("%d tabs survived, want 2", len(app.tabs))
	}
	if app.active != 1 {
		t.Errorf("active tab moved to %d, want 1", app.active)
	}
}
