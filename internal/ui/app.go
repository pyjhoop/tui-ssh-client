// Package ui is the Bubble Tea layer: layout, focus, and the state machine that
// ties the config store and ssh sessions together. It never touches the
// filesystem or the network directly — those calls go through config and ssh,
// always inside a tea.Cmd.
package ui

import (
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/vt"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

type focusArea int

const (
	focusSidebar focusArea = iota
	focusForm
	focusSession
)

type rightMode int

const (
	rightEmpty rightMode = iota
	rightForm
	rightTerminal
)

// escapeHint is the key that moves focus out of a live session.
const escapeHint = "ctrl+b"

// App is the root model.
type App struct {
	store *config.Store

	servers []model.Server
	sidebar sidebar
	form    form

	focus     focusArea
	rightMode rightMode

	width, height int

	// Session state. gen is bumped on every connect so output from a previous
	// session cannot land in the current emulator. The emulator and its input
	// pump are created once and reused across sessions — see keyPump.
	session     *sshpkg.Session
	emu         *vt.Emulator
	pump        keyPump
	pumpStarted sync.Once
	gen         int
	connectedID string
	// sessionName/sessionAddr label the terminal panel's title bar.
	sessionName string
	sessionAddr string

	connecting string
	status     string
	errMsg     string
	warning    string
	quitting   bool
}

// New builds the root model.
func New(store *config.Store) *App {
	return &App{
		store:     store,
		focus:     focusSidebar,
		rightMode: rightEmpty,
		sidebar:   newSidebar(nil, sidebarWidth-2, 10),
		form:      newForm(40, 20),
	}
}

func (a *App) Init() tea.Cmd {
	return loadServers(a.store)
}

// ── messages ────────────────────────────────────────────────────────────────

type serversLoadedMsg struct {
	servers []model.Server
	err     error
}

type serverSavedMsg struct {
	servers []model.Server
	warn    string
	err     error
}

type connectedMsg struct {
	gen     int
	session *sshpkg.Session
}

type connectFailedMsg struct {
	gen int
	err error
}

type outputMsg struct {
	gen  int
	data []byte
}

type sessionEndedMsg struct {
	gen int
	err error
}

// ── commands ────────────────────────────────────────────────────────────────

func loadServers(store *config.Store) tea.Cmd {
	return func() tea.Msg {
		servers, err := store.Load()
		return serversLoadedMsg{servers: servers, err: err}
	}
}

// saveServer persists the entry, writing a pasted key body to keys/<id>.pem
// first so only the path ends up in servers.json.
func saveServer(store *config.Store, srv model.Server, keyBody string) tea.Cmd {
	return func() tea.Msg {
		if keyBody != "" {
			path, err := store.SaveKey(srv.ID, keyBody)
			if err != nil {
				return serverSavedMsg{err: err}
			}
			srv.KeyPath = path
		}
		if err := srv.Validate(); err != nil {
			return serverSavedMsg{err: err}
		}

		var warn string
		if srv.Auth == model.AuthPassword && !store.PlaintextWarningSeen() {
			warn = "⚠ passwords are stored in plaintext in " + store.Path()
			if err := store.MarkPlaintextWarningSeen(); err != nil {
				return serverSavedMsg{err: err}
			}
		}

		if _, err := store.Add(srv); err != nil {
			return serverSavedMsg{err: err}
		}
		servers, err := store.Load()
		return serverSavedMsg{servers: servers, warn: warn, err: err}
	}
}

func connect(srv model.Server, gen, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		sess, err := sshpkg.Connect(srv, cols, rows)
		if err != nil {
			return connectFailedMsg{gen: gen, err: err}
		}
		return connectedMsg{gen: gen, session: sess}
	}
}

// waitForOutput is the standard Bubble Tea pump: one channel receive per
// command, rescheduled after each message.
func waitForOutput(sess *sshpkg.Session, gen int) tea.Cmd {
	return func() tea.Msg {
		data, ok := <-sess.Output()
		if !ok {
			return sessionEndedMsg{gen: gen, err: sess.ExitErr()}
		}
		return outputMsg{gen: gen, data: data}
	}
}

// ── update ──────────────────────────────────────────────────────────────────

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return a, a.resize(msg.Width, msg.Height)

	case serversLoadedMsg:
		if msg.err != nil {
			a.errMsg = msg.err.Error()
			return a, nil
		}
		a.servers = msg.servers
		a.sidebar.SetServers(a.servers)
		return a, nil

	case serverSavedMsg:
		if msg.err != nil {
			a.form.err = msg.err.Error()
			return a, nil
		}
		a.servers = msg.servers
		a.sidebar.SetServers(a.servers)
		a.warning = msg.warn
		a.status = "saved"
		a.rightMode = rightEmpty
		a.focus = focusSidebar
		return a, nil

	case connectedMsg:
		if msg.gen != a.gen {
			// A newer connect superseded this one.
			_ = msg.session.Close()
			return a, nil
		}
		a.session = msg.session
		a.connecting = ""
		a.rightMode = rightTerminal
		a.focus = focusSession
		a.status = fmt.Sprintf("connected · %s to return to the list", escapeHint)
		// Point the input pump at this session. Key events are encoded by the
		// emulator, so cursor-key modes stay correct.
		a.pump.attach(a.session)
		return a, waitForOutput(a.session, a.gen)

	case connectFailedMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		a.connecting = ""
		a.rightMode = rightEmpty
		a.focus = focusSidebar
		a.errMsg = msg.err.Error()
		return a, nil

	case outputMsg:
		if msg.gen != a.gen || a.emu == nil {
			return a, nil
		}
		_, _ = a.emu.Write(msg.data)
		return a, waitForOutput(a.session, a.gen)

	case sessionEndedMsg:
		if msg.gen != a.gen {
			return a, nil
		}
		a.teardownSession()
		a.rightMode = rightEmpty
		a.focus = focusSidebar
		if msg.err != nil {
			a.errMsg = "session ended: " + msg.err.Error()
		} else {
			a.status = "session closed"
		}
		return a, nil

	case tea.MouseMsg:
		return a, a.handleMouse(msg)

	case tea.KeyMsg:
		return a, a.handleKey(msg)
	}

	// Anything else (spinner ticks, cursor blinks) goes to the focused widget.
	switch a.focus {
	case focusForm:
		cmd, _ := a.form.Update(msg)
		return a, cmd
	case focusSidebar:
		return a, a.sidebar.Update(msg)
	}
	return a, nil
}

func (a *App) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Session focus swallows everything except the escape key: the remote shell
	// needs ctrl+c, ctrl+d, q and friends.
	if a.focus == focusSession {
		if msg.Type == tea.KeyCtrlB {
			a.focus = focusSidebar
			a.status = "session still running · select it again to go back"
			return nil
		}
		a.sendKey(msg)
		return nil
	}

	switch a.focus {
	case focusForm:
		cmd, action := a.form.Update(msg)
		switch action {
		case formCancel:
			a.rightMode = rightEmpty
			a.focus = focusSidebar
			return nil
		case formSubmit:
			srv, keyBody, err := a.form.Server()
			if err != nil {
				a.form.err = err.Error()
				return nil
			}
			if err := srv.Validate(); err != nil && keyBody == "" {
				a.form.err = err.Error()
				return nil
			}
			a.form.err = ""
			return saveServer(a.store, srv, keyBody)
		}
		return cmd

	default: // focusSidebar
		switch msg.String() {
		case "q", "ctrl+c":
			a.quitting = true
			a.teardownSession()
			return tea.Quit
		case "enter":
			return a.activateSelection()
		case "d":
			return a.deleteSelection()
		case "tab":
			if a.rightMode == rightTerminal && a.session != nil {
				a.focus = focusSession
				return nil
			}
			if a.rightMode == rightForm {
				a.focus = focusForm
				return nil
			}
			return nil
		}
		return a.sidebar.Update(msg)
	}
}

// activateSelection opens the form or connects, depending on the highlighted
// row.
func (a *App) activateSelection() tea.Cmd {
	it, ok := a.sidebar.Selected()
	if !ok {
		return nil
	}
	a.errMsg = ""
	a.status = ""

	if it.connect {
		w, h := a.rightInner()
		a.form = newForm(w, h)
		a.rightMode = rightForm
		a.focus = focusForm
		return nil
	}

	// Re-selecting the server we are already attached to just returns focus.
	if a.session != nil && a.rightMode == rightTerminal && a.connectedID == it.server.ID {
		a.focus = focusSession
		return nil
	}

	a.teardownSession()
	cols, rows := a.rightInner()
	a.gen++
	a.startTerminal(cols, rows)
	a.connectedID = it.server.ID
	a.sessionName = it.server.Title()
	a.sessionAddr = fmt.Sprintf("%s@%s", it.server.User, it.server.Addr())
	a.connecting = it.server.Title()
	a.rightMode = rightTerminal
	a.focus = focusSidebar
	return connect(it.server, a.gen, cols, rows)
}

func (a *App) deleteSelection() tea.Cmd {
	it, ok := a.sidebar.Selected()
	if !ok || it.connect {
		return nil
	}
	id := it.server.ID
	store := a.store
	return func() tea.Msg {
		if err := store.Remove(id); err != nil {
			return serversLoadedMsg{err: err}
		}
		servers, err := store.Load()
		return serversLoadedMsg{servers: servers, err: err}
	}
}

// sendKey pushes a key into the emulator, which encodes it and writes the bytes
// to the session through the pipe drained in connectedMsg.
func (a *App) sendKey(msg tea.KeyMsg) {
	if a.emu == nil || a.session == nil {
		return
	}
	if msg.Paste {
		a.emu.SendText(string(msg.Runes))
		return
	}
	if key, ok := keyToVT(msg); ok {
		a.emu.SendKey(key)
	}
}

func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		if a.focus == focusSidebar {
			return a.sidebar.Update(msg)
		}
		return nil
	}

	// The panel under the pointer takes focus.
	if msg.X < sidebarWidth {
		a.focus = focusSidebar
		// Rows inside the list start below the border and the title block.
		if idx, ok := a.rowToIndex(msg.Y); ok {
			a.sidebar.list.Select(idx)
			return a.activateSelection()
		}
		return a.sidebar.Update(msg)
	}

	switch a.rightMode {
	case rightForm:
		a.focus = focusForm
		// Translate the click into the form's own coordinates: past the sidebar,
		// the panel border, its title bar and the inner padding.
		local := msg
		local.X -= sidebarWidth + 1 + padX
		local.Y -= topMargin + 1 + rightHeaderRows
		cmd, _ := a.form.Update(local)
		return cmd
	case rightTerminal:
		if a.session != nil {
			a.focus = focusSession
		}
	}
	return nil
}

// listHeaderRows is how far the first list item sits below the top of the
// screen: the margin, the panel border, and the list's own title block (title
// plus the blank line under it). sidebarRowsPerItem is the default delegate's
// two rows plus its spacer. TestSidebarRowGeometry pins both.
const (
	listHeaderRows     = topMargin + 1 + 2
	sidebarRowsPerItem = 3
)

// rowToIndex maps a screen row inside the sidebar to a list index.
func (a *App) rowToIndex(y int) (int, bool) {
	rel := y - listHeaderRows
	if rel < 0 {
		return 0, false
	}
	idx := rel / sidebarRowsPerItem
	if idx >= len(a.servers)+1 {
		return 0, false
	}
	return idx, true
}

// resize recomputes the layout, the emulator geometry and the remote PTY size.
// All three must move together or the panel and the remote disagree.
func (a *App) resize(width, height int) tea.Cmd {
	// Some terminals report a zero size on the first message; fall back to the
	// classic 80x24 so we still draw something usable.
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	a.width, a.height = width, height

	a.sidebar.SetSize(sidebarWidth-borderSize-2*sidePadX, a.panelHeight())
	cols, rows := a.rightInner()
	a.form.setSize(cols-2*padX, rows)

	if a.emu != nil {
		a.emu.Resize(cols, rows)
	}
	if a.session != nil {
		sess := a.session
		return func() tea.Msg {
			_ = sess.Resize(cols, rows)
			return nil
		}
	}
	return nil
}

// The vertical budget is fixed: a top margin, the two panels, and one status
// row. Both panels get the same content height so their borders line up.
//
//	row 0            top margin
//	rows 1..h-2      sidebar and right panel (identical height)
//	row h-1          status line
const (
	topMargin  = 1
	statusRows = 1
	borderSize = 2 // one border row/column on each side
)

// panelHeight is the content height shared by both panels, borders excluded.
func (a *App) panelHeight() int {
	return maxInt(a.height-topMargin-statusRows-borderSize, 1)
}

// rightInner is the usable cell size of the right panel's body, borders and
// title bar excluded. It is also the emulator and remote PTY size.
func (a *App) rightInner() (cols, rows int) {
	cols = a.width - sidebarWidth - borderSize
	return maxInt(cols, 1), maxInt(a.panelHeight()-rightHeaderRows, 1)
}

// startTerminal readies the emulator for a fresh session, starting the input
// pump the first time round. The emulator is reused rather than recreated: see
// keyPump for why it must never be closed.
func (a *App) startTerminal(cols, rows int) {
	if a.emu == nil {
		a.emu = newEmulator(cols, rows)
	} else {
		resetEmulator(a.emu, cols, rows)
	}
	a.pumpStarted.Do(func() { go a.pump.run(a.emu) })
}

func (a *App) teardownSession() {
	// Detach first: the pump must stop writing to a session we are closing.
	a.pump.detach()
	if a.session != nil {
		_ = a.session.Close()
		a.session = nil
	}
	a.connectedID = ""
	a.sessionName, a.sessionAddr = "", ""
	a.gen++
}

// ── view ────────────────────────────────────────────────────────────────────

func (a *App) View() string {
	if a.quitting {
		return ""
	}
	if a.width == 0 || a.height == 0 {
		return "starting…"
	}

	cols, rows := a.rightInner()

	// Both panels are clamped to the exact same rectangle before the border is
	// applied. lipgloss only pads to the requested size, so content that runs
	// long would otherwise push one panel below the other.
	sideBody := lipgloss.NewStyle().Padding(0, sidePadX).Render(a.sidebar.View())
	left := panelStyle(a.focus == focusSidebar).
		Render(clampBlock(sideBody, sidebarWidth-borderSize, a.panelHeight()))

	// The right panel gets a title bar of its own, mirroring the sidebar's
	// "Servers" heading, so a live session always announces which host it is.
	rightContent := a.rightHeader(cols) + "\n" + clampBlock(a.rightBody(cols, rows), cols, rows)
	right := panelStyle(a.focus == focusSession || a.focus == focusForm).
		Render(clampBlock(rightContent, cols, a.panelHeight()))

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// Every row is padded to the full width so the whole frame is one clean
	// rectangle, margin row included.
	margin := strings.Repeat(padLine("", a.width)+"\n", topMargin)
	return margin + body + "\n" + padLine(a.statusLine(), a.width)
}

// clampBlock forces a block of styled text to exactly w columns by h rows.
func clampBlock(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, h)
	for i := range out {
		var line string
		if i < len(lines) {
			line = lines[i]
		}
		out[i] = padLine(line, w)
	}
	return strings.Join(out, "\n")
}

// padX insets the form and placeholder panels from the border. The terminal
// grid is deliberately excluded: it must fill the panel exactly or it stops
// matching the remote PTY size.
const (
	padX = 2
	// sidePadX keeps the list's selection bar off the sidebar border.
	sidePadX = 1
	// rightHeaderRows is the title bar plus the blank line under it, matching
	// how the sidebar list draws its own heading.
	rightHeaderRows = 2
)

// rightHeader renders the panel's title bar. In terminal mode it names the
// session, which is the whole point: the body should always say what you are
// looking at.
func (a *App) rightHeader(cols int) string {
	title, detail := "ssh-client", ""

	switch a.rightMode {
	case rightForm:
		title = "New connection"
	case rightTerminal:
		title = a.sessionName
		if a.connecting != "" {
			detail = "connecting…"
		} else {
			detail = a.sessionAddr
		}
	}

	line := strings.Repeat(" ", padX) + styleTitleBar.Render(title)
	if detail != "" {
		line += styleTitleDetail.Render("  " + detail)
	}
	return padLine(line, cols) + "\n" + padLine("", cols)
}

func (a *App) rightBody(cols, rows int) string {
	switch a.rightMode {
	case rightForm:
		return inset(a.form.View())

	case rightTerminal:
		if a.connecting != "" {
			return inset(fmt.Sprintf("connecting to %s…", a.connecting))
		}
		return renderEmulator(a.emu, cols, rows, a.focus == focusSession)

	default:
		var b strings.Builder
		b.WriteString("Pick a server on the left to open a session,\n")
		b.WriteString("or choose “+ Connect” to register a new one.\n\n")
		b.WriteString(styleHint.Render("↑/↓ move · enter select · d delete · q quit"))
		if a.errMsg != "" {
			b.WriteString("\n\n")
			b.WriteString(styleError.Render("✗ " + a.errMsg))
		}
		return inset(b.String())
	}
}

// inset applies the panel's horizontal padding. The title bar already supplies
// the vertical spacing, and clampBlock trims the result back to the panel width,
// so the padding never widens the layout.
func inset(s string) string {
	return lipgloss.NewStyle().PaddingLeft(padX).Render(s)
}

func (a *App) statusLine() string {
	switch {
	case a.warning != "":
		return styleWarning.Render(a.warning)
	case a.errMsg != "" && a.rightMode != rightEmpty:
		return styleError.Render("✗ " + a.errMsg)
	case a.status != "":
		return styleStatus.Render(a.status)
	default:
		return styleStatus.Render("tab focus panel · " + escapeHint + " leave session · q quit")
	}
}
