package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/pyjhoop/ssh-client/internal/config"
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

	// marked is the multi-selection, keyed by entry name. It is scoped to the
	// directory on screen: setEntries drops it, because a name carried over
	// from another directory would silently select the wrong file.
	marked map[string]bool
}

// setEntries installs a fresh listing, prepending ".." unless we are at the
// root, and puts the cursor back at the top.
func (p *filePane) setEntries(dir string, entries []model.FileEntry) {
	p.dir = dir
	p.err = ""
	p.marked = nil
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
// can actually copy. Directories are cargo now that copies recurse; ".." is
// still navigation and never moves.
func (p *filePane) transferable() (model.FileEntry, bool) {
	e, ok := p.selected()
	if !ok || e.Name == parentName {
		return model.FileEntry{}, false
	}
	return e, true
}

// toggleMark selects or deselects the row under the cursor. ".." is not a file,
// so it cannot be picked.
func (p *filePane) toggleMark() {
	e, ok := p.selected()
	if !ok || e.Name == parentName {
		return
	}
	if p.marked == nil {
		p.marked = map[string]bool{}
	}
	if p.marked[e.Name] {
		delete(p.marked, e.Name)
		return
	}
	p.marked[e.Name] = true
}

func (p *filePane) clearMarks() { p.marked = nil }

// targets returns what an operation on this pane would act on: the marked
// entries if there are any, otherwise just the row under the cursor.
//
// Every path — drag, keyboard transfer, delete — goes through this, so the
// selection means the same thing everywhere. Listing order is kept rather than
// selection order, so the confirmation reads the way the pane looks.
func (p *filePane) targets() []model.FileEntry {
	if len(p.marked) > 0 {
		out := make([]model.FileEntry, 0, len(p.marked))
		for _, e := range p.entries {
			if e.Name != parentName && p.marked[e.Name] {
				out = append(out, e)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if e, ok := p.transferable(); ok {
		return []model.FileEntry{e}
	}
	return nil
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
func (p *filePane) View(cols int, focused bool, dragging map[string]bool) string {
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
		out[i] = p.renderRow(p.entries[idx], cols, idx == p.cursor && focused, dragging[p.entries[idx].Name])
	}
	return strings.Join(out, "\n")
}

// renderRow lays out one entry: a marker, an icon and the name on the left, a
// human-readable size on the right, padded to exactly cols so the panel stays
// rectangular.
func (p *filePane) renderRow(e model.FileEntry, cols int, cursor, dragging bool) string {
	name := e.Name
	icon := "  "
	if e.IsDir {
		icon = "▸ "
		if e.Name != parentName {
			name += "/"
		}
	}
	// The marker is a column of its own, so a selected row never shifts the
	// name across by a cell.
	marked := p.marked[e.Name] && e.Name != parentName
	marker := " "
	if marked {
		marker = "●"
	}

	size := ""
	if !e.IsDir {
		size = humanSize(e.Size)
	}

	// Build the row unstyled first: the width arithmetic has to happen before any
	// ANSI gets involved.
	prefix := " " + marker + icon
	left := prefix + name
	gap := cols - len([]rune(left)) - len(size) - 1
	if gap < 1 {
		// Truncate the name rather than the size — the size is what makes two
		// same-named files distinguishable.
		keep := maxInt(cols-len(size)-len([]rune(prefix))-2, 1)
		left = ansiTruncate(left, keep+len([]rune(prefix)))
		gap = maxInt(cols-len([]rune(left))-len(size)-1, 1)
	}
	line := left + strings.Repeat(" ", gap) + size + " "

	switch {
	case cursor:
		return styleRowCursor.Render(padLine(line, cols))
	case dragging:
		return styleRowDragged.Render(padLine(line, cols))
	case marked:
		return styleRowMarked.Render(padLine(line, cols))
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

// dragState is what is being dragged between panes. Grabbing a selected row
// drags the whole selection; grabbing an unselected one drags just it, and the
// selection is left alone. over is the pane the pointer is currently above,
// which is what the release handler acts on.
type dragState struct {
	from    focusArea
	entries []model.FileEntry
	over    focusArea
}

// names is the set of rows the drag is carrying, for the pane to dim.
func (d *dragState) names() map[string]bool {
	set := make(map[string]bool, len(d.entries))
	for _, e := range d.entries {
		set[e.Name] = true
	}
	return set
}

func (d *dragState) label() string {
	if len(d.entries) == 1 {
		return d.entries[0].Name
	}
	return fmt.Sprintf("%d items", len(d.entries))
}

// transferReq is a planned transfer waiting for confirmation. The drag path and
// the keyboard path both produce one of these and nothing else, so the
// confirmation and the transfer itself stay shared code.
//
// sets is one entry per picked root, already walked: totals are known before the
// first byte moves, which is what makes the progress bar a percentage.
type transferReq struct {
	upload    bool
	entries   []model.FileEntry
	srcDir    string
	dstDir    string
	overwrite []string // destination names that already exist
	sets      []sftppkg.Set
	total     int64
	files     int
	dirs      int
	skipped   int
}

func (t transferReq) verb() string {
	if t.upload {
		return "Upload"
	}
	return "Download"
}

// single reports whether this is v2's case — one plain file — which keeps its
// original wording rather than the summary form.
func (t transferReq) single() bool { return len(t.entries) == 1 && t.dirs == 0 }

func (t transferReq) label() string {
	if len(t.entries) == 1 {
		return t.entries[0].Name
	}
	return fmt.Sprintf("%d items", len(t.entries))
}

// srcPath and dstPath only mean anything for a single root; the summary form
// names the directories instead.
func (t transferReq) srcPath() string {
	if len(t.sets) == 0 {
		return t.srcDir
	}
	return t.sets[0].SrcRoot
}

func (t transferReq) dstPath() string {
	if len(t.sets) == 0 {
		return t.dstDir
	}
	return t.sets[0].DstRoot
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
// openTab: bump the generation first so anything still in flight from a
// previous connection is dropped when it lands.
func (a *App) openSFTP() tea.Cmd {
	it, ok := a.sidebar.Selected()
	if !ok || !a.isServerRow(it) {
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
		connectSFTP(a.store, a.hostKeys, config.Inject(srv, a.secrets), gen),
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

	case " ":
		pane.toggleMark()
		pane.moveCursor(1)

	case "a":
		pane.clearMarks()

	case "t":
		return a.buildTransfer(side, pane.targets())

	case "d":
		return a.startDelete(side)

	case "R":
		a.startRename(side)

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
		// enter stays navigation for a directory: t is the key that copies one.
		if e.IsDir {
			if e.Name == parentName {
				return a.openDir(side, pane.br.Parent(pane.dir))
			}
			return a.openDir(side, pane.br.Join(pane.dir, e.Name))
		}
		return a.buildTransfer(side, pane.targets())
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
// through: it decides the direction and starts the walk that works out what
// would move and how many bytes that is.
//
// Walking a directory is network and file IO, so it cannot happen here — the
// request only exists once planTransfer answers. That is the one step v3 adds
// to v2's flow; everything after plannedMsg is shared as before.
//
// The overwrite check reads the destination pane's listing rather than asking
// the server, for the same reason: Update must not block on a round trip. A
// listing that is stale only costs a missing warning, never a wrong transfer.
func (a *App) buildTransfer(from focusArea, entries []model.FileEntry) tea.Cmd {
	src, dst := a.pane(from), a.pane(otherSide(from))
	if src == nil || dst == nil || src.br == nil || dst.br == nil || a.remote == nil {
		a.errMsg = "the remote pane is not connected yet"
		return nil
	}
	if a.transfer != nil || a.scanning {
		a.errMsg = "a transfer is already running"
		return nil
	}
	entries = copyable(entries)
	if len(entries) == 0 {
		return nil
	}

	existing := make(map[string]bool, len(dst.entries))
	for _, d := range dst.entries {
		existing[d.Name] = true
	}

	a.errMsg = ""
	a.scanning = true
	a.status = "scanning…"
	return planTransfer(planArgs{
		src:      src.br,
		dst:      dst.br,
		upload:   from == focusLocal,
		srcDir:   src.dir,
		dstDir:   dst.dir,
		entries:  entries,
		existing: existing,
		gen:      a.sftpGen,
	})
}

// copyable drops the rows that are navigation rather than cargo.
func copyable(entries []model.FileEntry) []model.FileEntry {
	out := entries[:0:0]
	for _, e := range entries {
		if e.Name != parentName {
			out = append(out, e)
		}
	}
	return out
}

// resolvePending answers the transfer alert. Like the confirm panel, keys it
// does not recognise are dropped rather than forwarded.
func (a *App) resolvePending(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "enter", "y", "Y":
		return a.startTransfer(*a.pending)
	case "esc", "n", "N", "q", "ctrl+c":
		a.pending = nil
		a.status = "transfer cancelled"
	}
	return nil
}

// startTransfer launches the confirmed copy: one context to cancel it with, one
// progress counter for the goroutine to write and the tick to read.
func (a *App) startTransfer(req transferReq) tea.Cmd {
	a.pending = nil

	ctx, cancel := context.WithCancel(context.Background())
	prog := &sftppkg.Progress{}
	prog.SetTotal(req.total)
	prog.SetName(req.label())

	a.transfer = &transferState{
		prog:    prog,
		cancel:  cancel,
		label:   req.label(),
		upload:  req.upload,
		started: time.Now(),
	}
	a.errMsg = ""
	a.status = ""
	return tea.Batch(runTransfer(ctx, a.remote, req, prog, a.sftpGen), tickProgress(a.sftpGen))
}

// cancelTransfer asks the running copy to stop. The state only clears when the
// goroutine reports back, so the status line says "cancelling…" in between
// rather than pretending it is already over.
func (a *App) cancelTransfer() {
	if a.transfer == nil {
		return
	}
	a.transfer.cancelling = true
	a.transfer.cancel()
}

// ── delete and rename ───────────────────────────────────────────────────────

// startDelete counts what a delete would remove before asking. A recursive
// delete must never go through on one keystroke, so the confirmation says how
// many files are inside the directories.
func (a *App) startDelete(side focusArea) tea.Cmd {
	pane := a.pane(side)
	if pane == nil || pane.br == nil {
		return nil
	}
	if a.transfer != nil || a.scanning {
		a.errMsg = "a transfer is already running"
		return nil
	}
	entries := copyable(pane.targets())
	if len(entries) == 0 {
		return nil
	}
	a.errMsg = ""
	a.scanning = true
	a.status = "scanning…"
	return planDelete(pane.br, side, pane.dir, entries, a.sftpGen)
}

// startRename replaces the pane body with a one-line input. It is not a new
// mode: like confirm and pending, it is non-nil state that eats every key until
// it is answered.
func (a *App) startRename(side focusArea) {
	pane := a.pane(side)
	if pane == nil || pane.br == nil {
		return
	}
	e, ok := pane.selected()
	if !ok || e.Name == parentName {
		return
	}
	in := textinput.New()
	in.SetValue(e.Name)
	in.CursorEnd()
	in.Focus()
	in.Prompt = ""
	a.errMsg = ""
	a.rename = &renameState{side: side, dir: pane.dir, from: e.Name, input: in}
}

// resolveRename answers the rename input. Keys it does not use go to the text
// field, never to the panes behind it.
func (a *App) resolveRename(msg tea.KeyMsg) tea.Cmd {
	r := a.rename
	switch msg.String() {
	case "esc":
		a.rename = nil
		a.status = "rename cancelled"
		return nil

	case "enter":
		name := strings.TrimSpace(r.input.Value())
		pane := a.pane(r.side)
		if name == "" || name == r.from || pane == nil || pane.br == nil {
			a.rename = nil
			return nil
		}
		if strings.ContainsAny(name, `/\`) {
			r.err = "the new name cannot contain a path separator"
			return nil
		}
		br, dir, from := pane.br, r.dir, r.from
		a.rename = nil
		a.status = "renaming…"
		return runRename(br, dir, from, name, a.sftpGen)
	}

	var cmd tea.Cmd
	r.err = ""
	r.input, cmd = r.input.Update(msg)
	return cmd
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
			// ".." is the only row that is navigation rather than cargo —
			// directories move now that copies recurse. Grabbing a selected row
			// takes the whole selection with it.
			if e, ok := pane.transferable(); ok {
				entries := []model.FileEntry{e}
				if pane.marked[e.Name] {
					entries = pane.targets()
				}
				a.drag = &dragState{from: side, entries: entries, over: side}
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
			return a.buildTransfer(d.from, d.entries), true
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

	// Only the pane the files are being dragged out of dims its source rows.
	var dragging map[string]bool
	if a.drag != nil && a.drag.from == side {
		dragging = a.drag.names()
	}

	body := p.View(cols, a.focus == side, dragging)
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
	case a.rename != nil:
		content = a.rename.View()
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
//
// One plain file keeps v2's exact wording; anything else gets the summary form,
// because naming twenty files is less use than counting them.
func (a *App) transferConfirm() *confirm {
	t := a.pending

	var warns []string
	if len(t.overwrite) > 0 {
		if t.single() {
			warns = append(warns, "⚠ the destination already exists — it will be overwritten")
		} else {
			warns = append(warns, "⚠ "+strings.Join(t.overwrite, ", ")+" already exist — they will be overwritten")
		}
	}
	if t.skipped > 0 {
		warns = append(warns, fmt.Sprintf("⚠ %s will be skipped", plural(t.skipped, "symlink")))
	}

	if t.single() {
		return &confirm{
			title: t.verb() + " file",
			lines: []string{
				fmt.Sprintf("%s  (%s)", t.entries[0].Name, humanSize(t.entries[0].Size)),
				"",
				"from  " + t.srcPath(),
				"to    " + t.dstPath(),
			},
			warn:   strings.Join(warns, "\n"),
			accept: "[enter] transfer",
		}
	}

	summary := plural(t.files, "file")
	if t.dirs > 0 {
		summary += ", " + plural(t.dirs, "directory")
	}
	return &confirm{
		title: fmt.Sprintf("%s %s", t.verb(), plural(len(t.entries), "item")),
		lines: []string{
			summary + "  ·  " + humanSize(t.total),
			"",
			"from  " + t.srcDir,
			"to    " + t.dstDir,
		},
		warn:   strings.Join(warns, "\n"),
		accept: "[enter] transfer",
	}
}

// plural counts a noun the way the confirmations read it. "directory" is the
// only irregular one we use.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if noun == "directory" {
		return fmt.Sprintf("%d directories", n)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// renameState is the one-line editor that replaces the pane body. It sits on
// the same axis as confirm and pending: non-nil means it owns the keyboard.
type renameState struct {
	side  focusArea
	dir   string
	from  string
	input textinput.Model
	err   string
}

func (r *renameState) View() string {
	var b strings.Builder
	b.WriteString(styleFormTitle.Render("Rename"))
	b.WriteString("\n\n")
	b.WriteString(styleFormLabel.Render("in  " + r.dir))
	b.WriteString("\n")
	b.WriteString(r.from + "  →")
	b.WriteString("\n")
	b.WriteString(r.input.View())
	b.WriteString("\n")
	if r.err != "" {
		b.WriteString("\n")
		b.WriteString(styleError.Render("✗ " + r.err))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(styleHint.Render("[enter] rename   [esc] cancel"))
	return b.String()
}

// ── progress bar ────────────────────────────────────────────────────────────

// transferStatus is the status line while a copy is running. It reads the
// atomic counters the transfer goroutine writes; the tick only exists to make
// this run again.
//
// The bar takes whatever width is left over after the text, and the caller pads
// the result to the frame — the bar may never be the thing that decides how wide
// the status line is.
func (a *App) transferStatus() string {
	t := a.transfer
	arrow := "↓"
	if t.upload {
		arrow = "↑"
	}
	name := t.prog.Name()
	if name == "" {
		name = t.label
	}
	done, total := t.prog.Done(), t.prog.Total()

	tail := " · ctrl+c cancel"
	if t.cancelling {
		tail = " · cancelling…"
	}

	var right string
	if total > 0 {
		pct := clampInt(int(done*100/total), 0, 100)
		right = fmt.Sprintf("  %3d%%  %s / %s", pct, humanSize(done), humanSize(total))
	} else {
		right = "  " + humanSize(done)
	}
	if secs := time.Since(t.started).Seconds(); secs > 0.5 && done > 0 {
		right += fmt.Sprintf("  %s/s", humanSize(int64(float64(done)/secs)))
	}

	head := arrow + " " + name + "  "
	barCols := a.width - ansi.StringWidth(head) - ansi.StringWidth(right) - ansi.StringWidth(tail)
	if barCols < 8 {
		// Too narrow for a bar: the numbers matter more than the picture.
		return styleStatus.Render(head + strings.TrimSpace(right) + tail)
	}
	return styleStatus.Render(head) + progressBar(barCols, done, total) + styleStatus.Render(right+tail)
}

// progressBar draws exactly cols cells. An unknown total renders as an empty
// track rather than a lie about how far along we are.
func progressBar(cols int, done, total int64) string {
	fill := 0
	if total > 0 {
		fill = clampInt(int(int64(cols)*done/total), 0, cols)
	}
	return styleBarFill.Render(strings.Repeat("▓", fill)) +
		styleBarEmpty.Render(strings.Repeat("░", cols-fill))
}
