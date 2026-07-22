package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/pyjhoop/ssh-client/internal/model"
	sshpkg "github.com/pyjhoop/ssh-client/internal/ssh"
)

// tabState is where a session is in its life. Only tabLost and tabReconnecting
// are new in v4: they exist because a dropped connection is no longer the end of
// the tab, it is a state the tab sits in while it tries to come back.
type tabState int

const (
	tabConnecting tabState = iota
	tabLive
	tabLost // the connection died; a retry is scheduled or waiting for [r]
	tabReconnecting
)

// backoff schedules the automatic retries: 1s, 2s, 4s, 8s, 16s, then 30s
// forever. There is no attempt limit on purpose — the usual reason a session
// drops is that the laptop was closed, and giving up after a few minutes would
// mean coming back to a dead tab either way. Closing it is one keystroke.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	d := time.Second << (attempt - 1)
	return min(d, maxBackoff)
}

const maxBackoff = 30 * time.Second

// sessionTab is one remote shell. Everything the root model used to hold as a
// single field — the session, its emulator, its scroll offset — lives here now,
// one copy per tab. Background tabs are ordinary tabs that happen not to be
// drawn: their output still arrives and still lands in their own emulator.
type sessionTab struct {
	id   string // model.Server.ID
	name string // label in the tab strip and the title bar
	addr string // user@host:port
	srv  model.Server

	// gen is this tab's generation, drawn from the app-wide counter. It changes
	// on every dial, including reconnects, so messages from a superseded attempt
	// find no tab and are dropped.
	gen     int
	session *sshpkg.Session
	slot    *termSlot

	state     tabState
	scrollOff int
	// activity is output that arrived while the tab was not on screen.
	activity bool

	// attempt counts consecutive failed reconnects; until is when the next one
	// fires, and is zero when nothing is scheduled (a host key that stopped
	// being trusted, say — that one waits for the user).
	attempt int
	until   time.Time
	lastErr error
}

func (t *sessionTab) emu() *vt.Emulator {
	if t == nil || t.slot == nil {
		return nil
	}
	return t.slot.emu
}

// down reports whether the tab has no usable session behind it.
func (t *sessionTab) down() bool {
	return t.state == tabLost || t.state == tabReconnecting
}

// label is the tab's text in the strip: a marker for a broken connection, and a
// dot for output the user has not seen.
func (t *sessionTab) label() string {
	name := t.name
	if t.down() {
		name = "⟳ " + name
	}
	if t.activity {
		name += " •"
	}
	return name
}

// ── tab bookkeeping ─────────────────────────────────────────────────────────

// cur is the tab on screen, or nil when there is none. Every caller has to
// handle nil: with tabs, "no session" is a normal state rather than a rare one.
func (a *App) cur() *sessionTab {
	if a.active < 0 || a.active >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.active]
}

func (a *App) curSession() *sshpkg.Session {
	if t := a.cur(); t != nil {
		return t.session
	}
	return nil
}

// tabByGen finds the tab a message belongs to. A miss means the message outlived
// its session, which is exactly what the old `msg.gen != a.gen` check caught.
func (a *App) tabByGen(gen int) (*sessionTab, bool) {
	for _, t := range a.tabs {
		if t.gen == gen {
			return t, true
		}
	}
	return nil, false
}

func (a *App) tabIndexFor(id string) int {
	for i, t := range a.tabs {
		if t.id == id {
			return i
		}
	}
	return -1
}

// dialing reports whether any connection is being established. The host key
// prompt uses it to tell a question somebody is waiting for from one left over
// from an attempt that has already been abandoned.
func (a *App) dialing() bool {
	if a.connectingSFTP != "" {
		return true
	}
	for _, t := range a.tabs {
		if t.state == tabConnecting || t.state == tabReconnecting {
			return true
		}
	}
	return false
}

// tabName keeps the strip readable when the same server is opened twice: the
// second session is "web-1 (2)".
func (a *App) tabName(srv model.Server) string {
	base := srv.Title()
	n := 0
	for _, t := range a.tabs {
		if t.id == srv.ID {
			n++
		}
	}
	if n == 0 {
		return base
	}
	return fmt.Sprintf("%s (%d)", base, n+1)
}

// switchTo brings a tab to the front. Nothing is sent to the remote and no
// screen state is rebuilt — the emulator has been keeping up all along.
func (a *App) switchTo(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	a.active = i
	t := a.tabs[i]
	t.activity = false
	a.rightMode = rightTerminal
	a.errMsg = ""
	// A tab that is down still takes focus: it is what you are looking at, and
	// [r] has to reach it.
	if t.session != nil || t.down() {
		a.focus = focusSession
	}
}

// cycleTab moves left or right through the strip, wrapping.
func (a *App) cycleTab(delta int) {
	if len(a.tabs) == 0 {
		return
	}
	n := len(a.tabs)
	a.switchTo(((a.active+delta)%n + n) % n)
}

// closeTab ends one session and gives its emulator back to the pool. The slot is
// recycled rather than dropped, which is what keeps the pump goroutines bounded.
func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	t := a.tabs[i]
	if t.session != nil {
		_ = t.session.Close()
		t.session = nil
	}
	a.pool.put(t.slot)
	t.slot = nil
	// A tick for a closed tab can still be in flight; no tab will claim its gen.
	t.gen = -1

	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	switch {
	case len(a.tabs) == 0:
		a.active = -1
		if a.rightMode == rightTerminal {
			a.rightMode = rightEmpty
			a.focus = focusSidebar
		}
	case a.active > i:
		a.active--
	case a.active == i:
		// Prefer the tab that moved into this slot, else the one before it.
		a.active = clampInt(i, 0, len(a.tabs)-1)
		a.tabs[a.active].activity = false
	}
	a.syncSidebarMarkers()
}

// syncSidebarMarkers keeps the list's ● markers in step with the open tabs, so
// the sidebar says which servers enter would switch to instead of dialling.
func (a *App) syncSidebarMarkers() {
	open := make(map[string]int, len(a.tabs))
	for _, t := range a.tabs {
		open[t.id]++
	}
	a.sidebar.SetOpen(open)
}

// teardownAllSessions is the quit path: every tab, not just the visible one.
func (a *App) teardownAllSessions() {
	for len(a.tabs) > 0 {
		a.closeTab(len(a.tabs) - 1)
	}
}

// ── opening and reconnecting ────────────────────────────────────────────────

// openTab dials srv in a new tab. force asks for a second session to a server
// that already has one; without it, re-selecting a connected server just shows
// the tab it is already in.
func (a *App) openTab(srv model.Server, force bool) tea.Cmd {
	if !force {
		if i := a.tabIndexFor(srv.ID); i >= 0 {
			a.switchTo(i)
			return nil
		}
	}

	cols, rows := a.rightInner()
	slot, ok := a.pool.get(cols, rows)
	if !ok {
		a.status = fmt.Sprintf("too many sessions (%d) · alt+w closes one", maxTabs)
		return nil
	}

	a.gen++
	t := &sessionTab{
		id:    srv.ID,
		name:  a.tabName(srv),
		addr:  fmt.Sprintf("%s@%s", srv.User, srv.Addr()),
		srv:   srv,
		gen:   a.gen,
		slot:  slot,
		state: tabConnecting,
	}
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
	a.syncSidebarMarkers()

	a.failErr = nil
	a.errMsg = ""
	a.lastAttempt = srv
	a.lastWasSFTP = false
	a.rightMode = rightTerminal
	a.focus = focusSidebar
	return connect(a.store, a.hostKeys, srv, t.gen, cols, rows)
}

// reconnect dials the tab's server again. Automatic attempts pass no prompt
// channel: a host key that is not on file must not be approved while nobody is
// looking, so an unattended retry fails instead of asking.
func (a *App) reconnect(t *sessionTab, auto bool) tea.Cmd {
	if t == nil {
		return nil
	}
	cols, rows := a.rightInner()
	a.gen++
	t.gen = a.gen
	t.state = tabReconnecting
	t.until = time.Time{}

	prompts := a.hostKeys
	if auto {
		prompts = nil
	}
	// Pick up any edit made to the entry since the session started.
	srv := t.srv
	for _, s := range a.servers {
		if s.ID == srv.ID {
			srv = s
			break
		}
	}
	t.srv = srv
	return connect(a.store, prompts, srv, t.gen, cols, rows)
}

// scheduleReconnect puts the tab into its waiting state and arms the next
// attempt. The tick carries the generation, so a retry that is overtaken by a
// manual [r] lands on no tab and is dropped.
func (a *App) scheduleReconnect(t *sessionTab, err error) tea.Cmd {
	t.state = tabLost
	t.session = nil
	t.lastErr = err
	t.attempt++
	if t.slot != nil {
		// Stop feeding keys to a session that is gone, but keep the screen: the
		// user should still be able to read what was on it.
		t.slot.pump.detach()
	}

	d := backoff(t.attempt)
	t.until = time.Now().Add(d)
	gen := t.gen
	return tea.Tick(d, func(time.Time) tea.Msg { return reconnectMsg{gen: gen} })
}

// stopReconnecting parks a tab that must not retry on its own — the host key
// case. The session stays as a tab so its last screen is still readable, and [r]
// is how it comes back, with the fingerprint prompt this time.
func (a *App) stopReconnecting(t *sessionTab, err error) {
	t.state = tabLost
	t.session = nil
	t.lastErr = err
	t.until = time.Time{}
	if t.slot != nil {
		t.slot.pump.detach()
	}
}

// ── keys ────────────────────────────────────────────────────────────────────

// tabKey handles the switching bindings, reporting whether it consumed the key.
//
// These are alt combinations rather than a tmux-style ctrl+b prefix so that
// ctrl+b keeps meaning exactly what it did before (leave the session), and so
// that no key ever has to be held while the app decides what it was for. A
// shell almost never wants alt+digit, which is what makes it safe to take.
func (a *App) tabKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	// A dialog owns the keyboard while it is up — tab switching included.
	if a.confirm != nil || a.pending != nil || a.rename != nil || a.sftpErr != nil {
		return nil, false
	}

	s := msg.String()
	switch s {
	case "alt+left", "alt+h":
		a.cycleTab(-1)
		return nil, true
	case "alt+right", "alt+l":
		a.cycleTab(1)
		return nil, true
	case "alt+w":
		if a.cur() == nil {
			return nil, false
		}
		a.closeTab(a.active)
		a.status = "session closed"
		return nil, true
	}

	if n, ok := strings.CutPrefix(s, "alt+"); ok && len(n) == 1 && n[0] >= '1' && n[0] <= '9' {
		idx := int(n[0] - '1')
		if idx < len(a.tabs) {
			a.switchTo(idx)
		}
		return nil, true
	}
	return nil, false
}

// ── rendering ───────────────────────────────────────────────────────────────

// tabStrip draws the open sessions into the right panel's existing title row.
// It deliberately costs no extra line: the vertical budget is fixed by
// panelHeight, and a strip that pushed the body down by one row would put the
// terminal grid out of step with the remote PTY.
//
// When more tabs are open than fit, the window slides to keep the active one
// visible and the cut sides are marked with ‹ ›.
func (a *App) tabStrip(width int) string {
	if len(a.tabs) == 0 || width <= 0 {
		return ""
	}

	segs := make([]string, len(a.tabs))
	widths := make([]int, len(a.tabs))
	for i, t := range a.tabs {
		style := styleTabIdle
		if i == a.active {
			style = styleTitleBar
		}
		segs[i] = style.Render(t.label())
		widths[i] = ansi.StringWidth(segs[i])
	}

	// Grow a window around the active tab while it still fits, right first so
	// the order on screen matches the order the tabs were opened in.
	lo := clampInt(a.active, 0, len(segs)-1)
	hi := lo
	used := widths[lo]
	for {
		grew := false
		if hi+1 < len(segs) && used+1+widths[hi+1] <= width-arrowRoom(lo, hi+1, len(segs)) {
			hi++
			used += 1 + widths[hi]
			grew = true
		}
		if lo > 0 && used+1+widths[lo-1] <= width-arrowRoom(lo-1, hi, len(segs)) {
			lo--
			used += 1 + widths[lo]
			grew = true
		}
		if !grew {
			break
		}
	}

	out := strings.Join(segs[lo:hi+1], " ")
	if lo > 0 {
		out = styleTabIdle.Render("‹") + out
	}
	if hi < len(segs)-1 {
		out += styleTabIdle.Render("›")
	}
	return out
}

// arrowRoom is the width the ‹ › markers would need for a given window.
func arrowRoom(lo, hi, n int) int {
	room := 0
	if lo > 0 {
		room++
	}
	if hi < n-1 {
		room++
	}
	return room
}

// tabStatus is the status line for a session that is not live: what happened,
// when the next attempt is, and how to take over.
func (a *App) tabStatus(t *sessionTab) string {
	switch {
	case t.state == tabConnecting:
		return styleStatus.Render("connecting to " + t.name + "…")
	case t.state == tabReconnecting:
		return styleWarning.Render("⟳ reconnecting to " + t.name + "… · [alt+w] close tab")
	case t.until.IsZero():
		reason := "connection lost"
		if t.lastErr != nil {
			reason = firstLineOf(t.lastErr)
		}
		return styleError.Render("✗ " + reason + " · [r] reconnect · [alt+w] close tab")
	default:
		in := max(int(time.Until(t.until).Round(time.Second)/time.Second), 0)
		return styleWarning.Render(fmt.Sprintf(
			"⟳ connection lost · reconnecting in %ds · [r] now · [alt+w] close tab", in))
	}
}
