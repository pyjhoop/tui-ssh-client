package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
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
	want := model.Server{Host: "example.com", Port: 2222, User: "deploy", Auth: model.AuthPassword, Password: "hunter2"}
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
	app.emu = newEmulator(cols, rows)
	_, _ = app.emu.Write([]byte("hello\r\n\x1b[31mred\x1b[0m"))
	out := renderEmulator(app.emu, cols, rows, true)
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

	app.rightMode = rightTerminal
	app.sessionName = "prod-web"
	app.sessionAddr = "deploy@10.0.0.1:22"
	app.emu = newEmulator(app.rightInner())

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
