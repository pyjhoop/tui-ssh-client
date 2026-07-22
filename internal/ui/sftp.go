package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/pyjhoop/ssh-client/internal/model"
	sftppkg "github.com/pyjhoop/ssh-client/internal/sftp"
)

// parentName is the row that walks up a level. It is drawn by the pane rather
// than coming from the browser, so both sides get it whatever the server lists.
const parentName = ".."

// filePane is one side of the split view: exactly one screen row per entry.
//
// bubbles/list is deliberately not used here. Its default delegate spends three
// rows per item, which is wrong for a file listing, and the arithmetic that maps
// a drop coordinate back to an entry only stays legible at one row per entry.
type filePane struct {
	br      sftppkg.Browser
	dir     string
	entries []model.FileEntry // entries[0] is ".." unless dir is the root
	cursor  int
	offset  int // first visible entry
	rows    int // body height, = panelHeight() - rightHeaderRows
	err     string
}

// setEntries installs a fresh listing, prepending ".." unless we are at the
// root, and puts the cursor back at the top.
func (p *filePane) setEntries(dir string, entries []model.FileEntry) {
	p.dir = dir
	p.err = ""
	list := make([]model.FileEntry, 0, len(entries)+1)
	if p.br != nil && p.br.Parent(dir) != dir {
		list = append(list, model.FileEntry{Name: parentName, IsDir: true})
	}
	p.entries = append(list, entries...)
	p.cursor = 0
	p.offset = 0
}

func (p *filePane) selected() (model.FileEntry, bool) {
	if p.cursor < 0 || p.cursor >= len(p.entries) {
		return model.FileEntry{}, false
	}
	return p.entries[p.cursor], true
}

// transferable reports the entry under the cursor only if it is something we
// can actually copy: ".." and directories are not.
func (p *filePane) transferable() (model.FileEntry, bool) {
	e, ok := p.selected()
	if !ok || e.IsDir || e.Name == parentName {
		return model.FileEntry{}, false
	}
	return e, true
}

// moveCursor moves by delta and scrolls just enough to keep the cursor visible.
func (p *filePane) moveCursor(delta int) {
	if len(p.entries) == 0 {
		p.cursor, p.offset = 0, 0
		return
	}
	p.cursor = clampInt(p.cursor+delta, 0, len(p.entries)-1)
	p.clampOffset()
}

func (p *filePane) clampOffset() {
	rows := maxInt(p.rows, 1)
	if len(p.entries) == 0 {
		p.offset = 0
		return
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
	p.offset = clampInt(p.offset, 0, maxInt(len(p.entries)-rows, 0))
}

// paneBodyTop is the first screen row of a pane's body: past the top margin,
// the panel border and the pane's own title bar. It is the same budget the
// terminal panel uses, which is what keeps the three panels aligned.
const paneBodyTop = topMargin + 1 + rightHeaderRows

// rowToIndex maps a screen row to an entry index, reporting false when the row
// is outside the listing. TestFilePaneRowGeometry checks it against what View
// actually draws.
func (p *filePane) rowToIndex(y int) (int, bool) {
	rel := y - paneBodyTop
	if rel < 0 || rel >= maxInt(p.rows, 1) {
		return 0, false
	}
	idx := rel + p.offset
	if idx >= len(p.entries) {
		return 0, false
	}
	return idx, true
}

// View renders the pane body — the title bar is drawn by the App, alongside the
// other panels', so all three share one header budget.
func (p *filePane) View(cols int, focused bool, dragged *model.FileEntry) string {
	rows := maxInt(p.rows, 1)
	out := make([]string, rows)

	if p.err != "" {
		out[0] = padLine(" "+styleError.Render("✗ "+p.err), cols)
		for i := 1; i < rows; i++ {
			out[i] = padLine("", cols)
		}
		return strings.Join(out, "\n")
	}

	for i := range out {
		idx := p.offset + i
		if idx >= len(p.entries) {
			out[i] = padLine("", cols)
			continue
		}
		out[i] = p.renderRow(p.entries[idx], cols, idx == p.cursor && focused, dragged)
	}
	return strings.Join(out, "\n")
}

// renderRow lays out one entry: an icon and name on the left, a human-readable
// size on the right, padded to exactly cols so the panel stays rectangular.
func (p *filePane) renderRow(e model.FileEntry, cols int, cursor bool, dragged *model.FileEntry) string {
	name := e.Name
	icon := "  "
	if e.IsDir {
		icon = "▸ "
		if e.Name != parentName {
			name += "/"
		}
	}

	size := ""
	if !e.IsDir {
		size = humanSize(e.Size)
	}

	// Build the row unstyled first: the width arithmetic has to happen before any
	// ANSI gets involved.
	left := " " + icon + name
	gap := cols - len([]rune(left)) - len(size) - 1
	if gap < 1 {
		// Truncate the name rather than the size — the size is what makes two
		// same-named files distinguishable.
		keep := maxInt(cols-len(size)-len([]rune(" "+icon))-2, 1)
		left = ansiTruncate(left, keep+len([]rune(" "+icon)))
		gap = maxInt(cols-len([]rune(left))-len(size)-1, 1)
	}
	line := left + strings.Repeat(" ", gap) + size + " "

	switch {
	case cursor:
		return styleRowCursor.Render(padLine(line, cols))
	case dragged != nil && dragged.Name == e.Name && !e.IsDir:
		return styleRowDragged.Render(padLine(line, cols))
	default:
		return padLine(line, cols)
	}
}

// ansiTruncate cuts a plain string to n runes. The rows are built before any
// styling, so rune counting is enough here.
func ansiTruncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:maxInt(n, 0)])
	}
	return string(r[:n-1]) + "…"
}

// humanSize is the right-hand column: short enough to never dominate the row.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit && exp < 3; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

// ── drag and transfer state ─────────────────────────────────────────────────

// dragState is a file being dragged between panes. over is the pane the pointer
// is currently above, which is what the release handler acts on.
type dragState struct {
	from  focusArea
	entry model.FileEntry
	over  focusArea
}

// transferReq is a transfer waiting for confirmation. The drag path and the
// keyboard path both produce one of these and nothing else, so the confirmation
// and the transfer itself are shared code.
type transferReq struct {
	upload    bool
	entry     model.FileEntry
	srcPath   string
	dstPath   string
	overwrite bool
}

func (t transferReq) verb() string {
	if t.upload {
		return "Upload"
	}
	return "Download"
}

// ── app wiring ──────────────────────────────────────────────────────────────

// pane resolves a side to its pane. Any other focus value has no pane, which is
// how the callers detect that the split view is not up.
func (a *App) pane(side focusArea) *filePane {
	switch side {
	case focusLocal:
		return &a.local
	case focusRemote:
		return &a.remotePane
	}
	return nil
}

// otherSide is the pane a transfer goes to.
func otherSide(side focusArea) focusArea {
	if side == focusLocal {
		return focusRemote
	}
	return focusLocal
}

// openSFTP attaches the split view to the highlighted server. It mirrors
// startConnect: bump the generation first so anything still in flight from a
// previous connection is dropped when it lands.
func (a *App) openSFTP() tea.Cmd {
	it, ok := a.sidebar.Selected()
	if !ok || it.connect {
		return nil
	}
	srv := it.server

	// Already attached to this server: just show the panes again.
	if a.remote != nil && a.sftpID == srv.ID {
		a.rightMode = rightSFTP
		a.focus = focusLocal
		a.errMsg = ""
		return nil
	}
	return a.startSFTP(srv)
}

// startSFTP is openSFTP without the sidebar lookup, so the error card can retry
// the connection the user actually asked for.
func (a *App) startSFTP(srv model.Server) tea.Cmd {
	a.teardownSFTP() // bumps sftpGen
	gen := a.sftpGen

	a.sftpID = srv.ID
	a.sftpName = srv.Title()
	a.sftpAddr = fmt.Sprintf("%s@%s", srv.User, srv.Addr())
	a.connectingSFTP = srv.Title()
	a.lastAttempt = srv
	a.lastWasSFTP = true
	a.failErr = nil
	a.sftpErr = nil
	a.rightMode = rightSFTP
	a.focus = focusLocal
	a.errMsg = ""
	a.status = ""

	_, rows := a.rightInner()
	a.local = filePane{br: sftppkg.Local{}, rows: rows}
	a.remotePane = filePane{rows: rows}

	home, err := a.local.br.Home()
	if err != nil {
		home = "/"
	}
	return tea.Batch(
		listDir(a.local.br, focusLocal, home, gen),
		connectSFTP(a.store, a.hostKeys, srv, gen),
	)
}

// refreshPanes re-reads both directories. It runs after a transfer so the file
// shows up where it landed without the user pressing r.
func (a *App) refreshPanes() tea.Cmd {
	var cmds []tea.Cmd
	if a.local.br != nil && a.local.dir != "" {
		cmds = append(cmds, listDir(a.local.br, focusLocal, a.local.dir, a.sftpGen))
	}
	if a.remotePane.br != nil && a.remotePane.dir != "" {
		cmds = append(cmds, listDir(a.remotePane.br, focusRemote, a.remotePane.dir, a.sftpGen))
	}
	return tea.Batch(cmds...)
}

// handleSFTPKey is the keyboard half of the split view. The keyboard and the
// drag path meet at buildTransfer and share everything after it.
func (a *App) handleSFTPKey(msg tea.KeyMsg) tea.Cmd {
	side := a.focus
	pane := a.pane(side)
	if pane == nil {
		a.focus = focusSidebar
		return nil
	}

	// A floating error card owns the keyboard while it is up, exactly like the
	// confirm panel: keys it does not recognise are dropped rather than reaching
	// the panes behind it.
	if a.sftpErr != nil {
		switch msg.String() {
		case "r":
			return a.retryConnect()
		case "e":
			srv := a.lastAttempt
			a.dismissSFTPError()
			return a.editServer(srv)
		case "esc":
			a.dismissSFTPError()
		}
		return nil
	}

	switch msg.String() {
	case escapeHint, "esc":
		a.focus = focusSidebar
		a.status = "sftp still connected · f to go back"
		return nil

	case "tab", "left", "right", "h", "l":
		a.focus = otherSide(side)
		return nil

	case "up", "k":
		pane.moveCursor(-1)
	case "down", "j":
		pane.moveCursor(1)
	case "pgup":
		pane.moveCursor(-maxInt(pane.rows-1, 1))
	case "pgdown":
		pane.moveCursor(maxInt(pane.rows-1, 1))
	case "home":
		pane.moveCursor(-len(pane.entries))
	case "end":
		pane.moveCursor(len(pane.entries))

	case "backspace":
		return a.openDir(side, pane.br.Parent(pane.dir))

	case "r":
		return listDir(pane.br, side, pane.dir, a.sftpGen)

	case "enter":
		e, ok := pane.selected()
		if !ok {
			return nil
		}
		if e.IsDir {
			if e.Name == parentName {
				return a.openDir(side, pane.br.Parent(pane.dir))
			}
			return a.openDir(side, pane.br.Join(pane.dir, e.Name))
		}
		a.buildTransfer(side, e)

	case " ":
		// space always means "transfer this", so a directory gets an explicit
		// refusal rather than silently walking into it.
		if e, ok := pane.selected(); ok {
			a.buildTransfer(side, e)
		}
	}
	return nil
}

func (a *App) openDir(side focusArea, dir string) tea.Cmd {
	pane := a.pane(side)
	if pane == nil || pane.br == nil {
		return nil
	}
	return listDir(pane.br, side, dir, a.sftpGen)
}

// buildTransfer is the single funnel both the drag and the keyboard path go
// through: it decides the direction, builds both paths and works out whether
// something is about to be overwritten.
//
// The overwrite check reads the destination pane's listing rather than asking
// the server, because Update must not block on a round trip. A listing that is
// stale only costs a missing warning, never a wrong transfer.
func (a *App) buildTransfer(from focusArea, e model.FileEntry) {
	src, dst := a.pane(from), a.pane(otherSide(from))
	if src == nil || dst == nil || src.br == nil || dst.br == nil || a.remote == nil {
		a.errMsg = "the remote pane is not connected yet"
		return
	}
	if a.busy {
		a.errMsg = "a transfer is already running"
		return
	}
	if e.IsDir || e.Name == parentName {
		a.errMsg = e.Name + ": " + sftppkg.ErrIsDir.Error()
		return
	}

	req := transferReq{
		upload:  from == focusLocal,
		entry:   e,
		srcPath: src.br.Join(src.dir, e.Name),
		dstPath: dst.br.Join(dst.dir, e.Name),
	}
	for _, d := range dst.entries {
		if d.Name == e.Name {
			req.overwrite = true
			break
		}
	}

	a.errMsg = ""
	a.pending = &req
}

// resolvePending answers the transfer alert. Like the confirm panel, keys it
// does not recognise are dropped rather than forwarded.
func (a *App) resolvePending(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter", "y", "Y":
		req := *a.pending
		a.pending = nil
		a.busy = true
		a.status = "transferring " + req.entry.Name + "…"
		return runTransfer(a.remote, req, a.sftpGen)
	case "esc", "n", "N", "q", "ctrl+c":
		a.pending = nil
		a.status = "transfer cancelled"
	}
	return nil
}

// handleSFTPMouse implements the three stages of a drag. It reports whether it
// consumed the event; anything it does not take falls through to the ordinary
// mouse handling, which is what keeps sidebar clicks working.
func (a *App) handleSFTPMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	side, overPane := a.sideAt(msg.X)

	switch msg.Action {
	case tea.MouseActionPress:
		switch msg.Button {
		case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
			if !overPane {
				return nil, false
			}
			delta := scrollStep
			if msg.Button == tea.MouseButtonWheelUp {
				delta = -scrollStep
			}
			a.pane(side).moveCursor(delta)
			return nil, true

		case tea.MouseButtonLeft:
			if !overPane {
				return nil, false // a sidebar click, not ours
			}
			a.focus = side
			pane := a.pane(side)
			idx, in := pane.rowToIndex(msg.Y)
			if !in {
				return nil, true
			}
			pane.cursor = idx
			// Only files start a drag: ".." and directories are navigation.
			if e, ok := pane.transferable(); ok {
				a.drag = &dragState{from: side, entry: e, over: side}
			}
			return nil, true
		}
		return nil, false

	case tea.MouseActionMotion:
		if a.drag == nil {
			return nil, false
		}
		if overPane {
			a.drag.over = side
		} else {
			a.drag.over = focusSidebar
		}
		return nil, true

	case tea.MouseActionRelease:
		// Terminals disagree about which button a release reports — some say
		// MouseButtonNone. Judge by whether a drag was in flight, never by the
		// button value.
		if a.drag == nil {
			return nil, false
		}
		d := *a.drag
		a.drag = nil
		if overPane {
			d.over = side
		}
		if d.over == otherSide(d.from) && overPane {
			a.buildTransfer(d.from, d.entry)
		}
		return nil, true
	}
	return nil, false
}

// sideAt maps an x coordinate to a pane, reporting false over the sidebar. The
// split is the same arithmetic View uses, so a click always lands where it
// looks like it should.
func (a *App) sideAt(x int) (focusArea, bool) {
	if x < sidebarWidth {
		return focusSidebar, false
	}
	localOuter, _ := a.sftpWidths()
	if x < sidebarWidth+localOuter {
		return focusLocal, true
	}
	return focusRemote, true
}

// sftpWidths splits the right area in two. The remainder goes to the remote
// pane so the pair always adds up to the full width on odd terminals.
func (a *App) sftpWidths() (localOuter, remoteOuter int) {
	total := maxInt(a.width-sidebarWidth, 2*(borderSize+1))
	localOuter = total / 2
	return localOuter, total - localOuter
}

// ── rendering ───────────────────────────────────────────────────────────────

func (a *App) sftpPanels(rows int) string {
	localOuter, remoteOuter := a.sftpWidths()
	left := a.renderPane(&a.local, maxInt(localOuter-borderSize, 1), focusLocal, rows)
	right := a.renderPane(&a.remotePane, maxInt(remoteOuter-borderSize, 1), focusRemote, rows)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

func (a *App) renderPane(p *filePane, cols int, side focusArea, rows int) string {
	label, dir := "Local", p.dir
	if side == focusRemote {
		label = a.sftpAddr
		if label == "" {
			label = "Remote"
		}
		if a.connectingSFTP != "" {
			dir = "connecting…"
		}
	}

	// Only the pane the file is being dragged out of dims its source row.
	var dragged *model.FileEntry
	if a.drag != nil && a.drag.from == side {
		dragged = &a.drag.entry
	}

	body := p.View(cols, a.focus == side, dragged)
	content := paneHeader(cols, label, dir) + "\n" + clampBlock(body, cols, rows)

	style := panelStyle(a.focus == side)
	// While a drag is in flight the border says where the file would land.
	if a.drag != nil && a.drag.over == side && a.drag.from != side {
		style = dropStyle()
	}
	return style.Render(clampBlock(content, cols, a.panelHeight()))
}

// paneHeader costs exactly rightHeaderRows, the same budget the terminal
// panel's title bar uses — that is what keeps all three panels aligned.
func paneHeader(cols int, label, dir string) string {
	title := strings.Repeat(" ", padX) + styleTitleBar.Render(label)
	path := strings.Repeat(" ", padX) + styleTitleDetail.Render(dir)
	return padLine(title, cols) + "\n" + padLine(path, cols)
}

// modalPad is the dialog's own inset from its border. It is deliberately not
// padX: this box floats, so it is sized to its content rather than to a panel.
const (
	modalPadX = 2
	modalPadY = 1
	// modalMinCols keeps a short message from rendering as a sliver.
	modalMinCols = 28
)

// sftpModal builds the floating dialog and where to put it, or an empty string
// when nothing is being asked. Priority runs newest question first: a transfer
// the user just started outranks a connection error they have already seen.
func (a *App) sftpModal() (box string, x, y int) {
	var content string
	switch {
	case a.pending != nil:
		content = a.transferConfirm().View()
	case a.confirm != nil:
		content = a.confirm.View()
	case a.sftpErr != nil:
		content = errorCard(a.sftpErr, a.lastAttempt)
	default:
		return "", 0, 0
	}

	// The box may not outgrow the frame it floats in, borders included.
	maxCols := maxInt(a.width-2*borderSize-2, modalMinCols)
	maxRows := maxInt(a.panelHeight()-2, 1)

	lines := strings.Split(content, "\n")
	cols := modalMinCols
	for _, l := range lines {
		cols = maxInt(cols, ansi.StringWidth(l))
	}
	cols = clampInt(cols, 1, maxCols)
	rows := clampInt(len(lines)+2*modalPadY, 1, maxRows)

	// Pad each row by hand rather than with a lipgloss padding style: clampBlock
	// has already fixed the width, and a padding style would widen it again.
	body := strings.Split(clampBlock(content, cols, rows-2*modalPadY), "\n")
	inner := make([]string, 0, rows)
	blank := strings.Repeat(" ", cols+2*modalPadX)
	for range modalPadY {
		inner = append(inner, blank)
	}
	for _, l := range body {
		inner = append(inner, strings.Repeat(" ", modalPadX)+l+strings.Repeat(" ", modalPadX))
	}
	for range modalPadY {
		inner = append(inner, blank)
	}

	box = styleModal.Render(strings.Join(inner, "\n"))

	// Centre it over the whole frame, then keep it inside the panels' rows.
	boxCols := cols + 2*modalPadX + borderSize
	boxRows := rows + borderSize
	x = maxInt((a.width-boxCols)/2, 0)
	y = topMargin + maxInt((a.panelHeight()+borderSize-boxRows)/2, 0)
	return box, x, y
}

// dismissSFTPError clears the floating error card. The panes underneath were
// never taken down, so there is nothing else to restore.
func (a *App) dismissSFTPError() {
	a.sftpErr = nil
	a.failErr = nil
	a.errMsg = ""
}

// transferConfirm renders the pending transfer through the shared confirm
// panel, so the alert looks like every other confirmation in the app.
func (a *App) transferConfirm() *confirm {
	t := a.pending
	warn := ""
	if t.overwrite {
		warn = "⚠ the destination already exists — it will be overwritten"
	}
	return &confirm{
		title: t.verb() + " file",
		lines: []string{
			fmt.Sprintf("%s  (%s)", t.entry.Name, humanSize(t.entry.Size)),
			"",
			"from  " + t.srcPath,
			"to    " + t.dstPath,
		},
		warn:   warn,
		accept: "[enter] transfer",
	}
}
