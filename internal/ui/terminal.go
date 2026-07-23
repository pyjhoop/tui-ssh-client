package ui

import (
	"io"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// newEmulator creates the virtual terminal that owns all session screen state.
func newEmulator(cols, rows int) *vt.Emulator {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	return vt.NewEmulator(cols, rows)
}

// resetEmulator wipes the screen for a new session. ESC c is RIS, a full
// terminal reset; the emulator handles it like a real terminal would.
func resetEmulator(emu *vt.Emulator, cols, rows int) {
	_, _ = emu.Write([]byte("\x1bc"))
	emu.Resize(maxInt(cols, 1), maxInt(rows, 1))
}

// maxTabs is the ceiling on open sessions, and therefore on emulators and pump
// goroutines. It lives here rather than in the tab code because the pool is what
// actually enforces it: a slot is never destroyed, only recycled.
const maxTabs = 8

// termSlot is one emulator plus the pump goroutine draining its reply pipe.
// Neither is ever closed — see keyPump — so a tab borrows a slot and gives it
// back instead of creating and destroying one.
type termSlot struct {
	emu  *vt.Emulator
	pump *keyPump
}

// termPool hands slots out and takes them back. Because closed tabs return
// their slot, the number of live pump goroutines is bounded by how many sessions
// are open at once, not by how many have been opened over the life of the
// program.
type termPool struct {
	free []*termSlot
	live int
}

// get borrows a slot sized for the panel, reporting false once maxTabs are out.
func (p *termPool) get(cols, rows int) (*termSlot, bool) {
	if n := len(p.free); n > 0 {
		s := p.free[n-1]
		p.free = p.free[:n-1]
		resetEmulator(s.emu, cols, rows)
		return s, true
	}
	if p.live >= maxTabs {
		return nil, false
	}
	s := &termSlot{emu: newEmulator(cols, rows), pump: &keyPump{}}
	p.live++
	go s.pump.run(s.emu)
	return s, true
}

// put returns a slot. The screen is wiped here as well as in get so a recycled
// emulator never hands the next session the last one's output.
func (p *termPool) put(s *termSlot) {
	if s == nil {
		return
	}
	s.pump.detach()
	_, _ = s.emu.Write([]byte("\x1bc"))
	p.free = append(p.free, s)
}

// keyPump owns the emulator's input pipe.
//
// The emulator answers terminal queries (ESC[6n and friends, which bash and vim
// send constantly) by writing the reply to that pipe from inside emu.Write —
// which runs on the UI goroutine. If nothing is reading, the next query
// deadlocks the whole program.
//
// The pump deliberately outlives individual sessions: vt.Emulator.Close is the
// only way to interrupt a blocked Read, and inside the library that Close races
// with the Read it is trying to interrupt. So we keep one emulator and one pump
// for the life of the program and just re-point the pump at the current session.
type keyPump struct {
	mu sync.Mutex
	w  io.Writer
}

// attach points the pump at a session; detach sends its bytes nowhere.
func (p *keyPump) attach(w io.Writer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.w = w
}

func (p *keyPump) detach() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.w = nil
}

func (p *keyPump) run(emu *vt.Emulator) {
	buf := make([]byte, 4096)
	for {
		n, err := emu.Read(buf)
		if n > 0 {
			p.mu.Lock()
			w := p.w
			p.mu.Unlock()
			if w != nil {
				// A failed write means the session is going away; the UI will
				// detach us shortly. Keep reading either way.
				_, _ = w.Write(buf[:n])
			}
		}
		if err != nil {
			return
		}
	}
}

// renderEmulator turns the vt cell grid into a string of exactly rows lines,
// each padded to cols columns. Styles survive as ANSI sequences; when showCursor
// is set the cell under the cursor is drawn reversed.
func renderEmulator(emu *vt.Emulator, cols, rows int, showCursor bool) string {
	if emu == nil || cols < 1 || rows < 1 {
		return strings.Repeat("\n", maxInt(rows-1, 0))
	}

	restore := func() {}
	if showCursor {
		restore = highlightCursor(emu)
	}
	out := emu.Render()
	restore()

	lines := strings.Split(out, "\n")
	rendered := make([]string, rows)
	for i := range rendered {
		var line string
		if i < len(lines) {
			line = lines[i]
		}
		rendered[i] = padLine(line, cols)
	}
	return strings.Join(rendered, "\n")
}

// scrollStep is how far one wheel notch moves the viewport.
const scrollStep = 3

// renderScrolled draws the terminal with the viewport lifted offset lines into
// the scrollback. offset == 0 is the live screen and takes the fast path.
//
// Visible row i shows scrollback line ScrollbackLen()-offset+i while that index
// is still in the buffer, and screen row i-offset afterwards. The cursor is not
// drawn while scrolled: it lives on the live screen, which may not even be on
// display.
func renderScrolled(emu *vt.Emulator, cols, rows, offset int, showCursor bool) string {
	if offset <= 0 || emu == nil {
		return renderEmulator(emu, cols, rows, showCursor)
	}
	if cols < 1 || rows < 1 {
		return strings.Repeat("\n", maxInt(rows-1, 0))
	}

	sb := emu.Scrollback()
	offset = clampInt(offset, 0, sb.Len())
	screen := strings.Split(emu.Render(), "\n")

	out := make([]string, rows)
	for i := range out {
		var line string
		switch {
		case i < offset:
			// Scrollback lines are stored trimmed of trailing blanks, so a nil
			// line here is simply an empty one.
			line = sb.Line(sb.Len() - offset + i).Render()
		case i-offset < len(screen):
			line = screen[i-offset]
		}
		out[i] = padLine(line, cols)
	}
	return strings.Join(out, "\n")
}

// renderSelected draws the same viewport as renderScrolled with the selected
// cells reversed. The cursor is not drawn while a selection is up: reversing a
// cell twice puts it back, so the cursor would vanish exactly where it overlaps.
func renderSelected(emu *vt.Emulator, cols, rows, offset int, sel *selection, showCursor bool) string {
	if sel == nil || emu == nil {
		return renderScrolled(emu, cols, rows, offset, showCursor)
	}
	restore := highlightSelection(emu, cols, rows, offset, *sel)
	defer restore()
	return renderScrolled(emu, cols, rows, offset, false)
}

// highlightSelection flips the selected cells to reverse video and returns a
// func that puts them back — highlightCursor's trick, over a range. The emulator
// owns the screen here as everywhere: there is no shadow copy of the selection,
// only cells that are briefly drawn differently.
func highlightSelection(emu *vt.Emulator, cols, rows, offset int, sel selection) func() {
	var undo []func()
	from, to := sel.ordered()
	for y := maxInt(from.y, 0); y <= minInt(to.y, rows-1); y++ {
		startX, endX := selRange(y, from, to, cols)
		for x := startX; x <= endX; x++ {
			cell, set := selCell(emu, offset, x, y)
			if cell == nil || cell.IsZero() {
				// Nothing to reverse: either past the end of a stored scrollback
				// line, or the tail of a double-width grapheme, which the head
				// already draws.
				continue
			}
			original := cell.Clone()
			shown := cell.Clone()
			if shown.Content == "" {
				shown.Content = " "
				shown.Width = 1
			}
			shown.Style.Attrs |= uv.AttrReverse
			set(shown)
			undo = append(undo, func() { set(original) })
		}
	}
	return func() {
		for _, f := range undo {
			f()
		}
	}
}

// selectedText is the selection as plain text: no styling, trailing blanks
// dropped, rows joined with newlines.
//
// Soft-wrapped lines are deliberately not rejoined. The emulator does not record
// whether a row ran over, so guessing would splice text back together in the
// wrong places; what is copied is what is on screen.
func selectedText(emu *vt.Emulator, cols, rows, offset int, sel *selection) string {
	if sel == nil || emu == nil || cols < 1 || rows < 1 {
		return ""
	}
	from, to := sel.ordered()

	var lines []string
	for y := maxInt(from.y, 0); y <= minInt(to.y, rows-1); y++ {
		startX, endX := selRange(y, from, to, cols)
		var row strings.Builder
		for x := startX; x <= endX; x++ {
			cell, _ := selCell(emu, offset, x, y)
			switch {
			case cell == nil, cell.Content == "" && !cell.IsZero():
				row.WriteString(" ")
			case cell.IsZero():
				// The tail of a double-width grapheme: its text came with the head.
			default:
				row.WriteString(cell.Content)
			}
		}
		lines = append(lines, strings.TrimRight(row.String(), " "))
	}
	return strings.Join(lines, "\n")
}

// selRange is the inclusive column span of one row of a linear selection:
// the first row starts at the anchor, the last ends at the cursor, and the rows
// between are whole. A rectangular (column block) selection would be a different
// function, and is not what a terminal selection means.
func selRange(y int, from, to point, cols int) (startX, endX int) {
	startX, endX = 0, cols-1
	if y == from.y {
		startX = clampInt(from.x, 0, cols-1)
	}
	if y == to.y {
		endX = clampInt(to.x, 0, cols-1)
	}
	return startX, endX
}

// selCell resolves a viewport cell to the storage behind it, with a setter for
// writing it back. It is the one place that knows a visible row may come from
// the scrollback or from the live screen — the same split renderScrolled draws.
func selCell(emu *vt.Emulator, offset, x, y int) (*uv.Cell, func(*uv.Cell)) {
	if offset > 0 && y < offset {
		line := emu.ScrollbackLen() - offset + y
		return emu.ScrollbackCellAt(x, line), func(c *uv.Cell) {
			if p := emu.ScrollbackCellAt(x, line); p != nil {
				*p = *c
			}
		}
	}
	sy := y - maxInt(offset, 0)
	return emu.CellAt(x, sy), func(c *uv.Cell) { emu.SetCell(x, sy, c) }
}

// maxScrollOffset is how far back the viewport may go.
func maxScrollOffset(emu *vt.Emulator) int {
	if emu == nil || emu.IsAltScreen() {
		// The scrollback belongs to the main screen; scrolling an alt-screen app
		// like vim would show unrelated history.
		return 0
	}
	return emu.ScrollbackLen()
}

// altScreenScroll converts a wheel notch into cursor keys, which is what a real
// terminal does for full-screen apps that have not asked for mouse reporting.
func altScreenScroll(emu *vt.Emulator, up bool) {
	code := uv.KeyDown
	if up {
		code = uv.KeyUp
	}
	for range scrollStep {
		emu.SendKey(vt.KeyPressEvent{Code: code})
	}
}

// highlightCursor flips the cursor cell to reverse video and returns a func
// that puts the original cell back. The emulator is the single owner of screen
// state, so we mutate and restore rather than keeping a shadow copy.
func highlightCursor(emu *vt.Emulator) func() {
	pos := emu.CursorPosition()
	cell := emu.CellAt(pos.X, pos.Y)
	if cell == nil {
		return func() {}
	}
	original := cell.Clone()

	shown := cell.Clone()
	if shown.Content == "" {
		shown.Content = " "
		shown.Width = 1
	}
	shown.Style.Attrs |= uv.AttrReverse
	emu.SetCell(pos.X, pos.Y, shown)

	return func() { emu.SetCell(pos.X, pos.Y, original) }
}

// padLine pads (or truncates) a styled line to exactly width columns.
func padLine(line string, width int) string {
	w := ansi.StringWidth(line)
	switch {
	case w == width:
		return line
	case w < width:
		return line + strings.Repeat(" ", width-w)
	default:
		return ansi.Truncate(line, width, "")
	}
}

// keyToVT is the single conversion table from Bubble Tea key events to
// ultraviolet key events. The emulator turns those into the right ANSI bytes,
// which keeps application-cursor-key mode (vim, less) working.
func keyToVT(msg tea.KeyMsg) (vt.KeyPressEvent, bool) {
	key := vt.KeyPressEvent{}
	if msg.Alt {
		key.Mod |= vt.ModAlt
	}

	switch msg.Type {
	case tea.KeyRunes:
		if len(msg.Runes) == 0 {
			return key, false
		}
		key.Code = msg.Runes[0]
		key.Text = string(msg.Runes)

	case tea.KeySpace:
		key.Code = uv.KeySpace
		key.Text = " "
	case tea.KeyEnter:
		key.Code = uv.KeyEnter
	case tea.KeyTab:
		key.Code = uv.KeyTab
	case tea.KeyShiftTab:
		key.Code = uv.KeyTab
		key.Mod |= vt.ModShift
	case tea.KeyEsc:
		key.Code = uv.KeyEscape
	case tea.KeyBackspace:
		key.Code = uv.KeyBackspace
	case tea.KeyDelete:
		key.Code = uv.KeyDelete
	case tea.KeyInsert:
		key.Code = uv.KeyInsert

	case tea.KeyUp:
		key.Code = uv.KeyUp
	case tea.KeyDown:
		key.Code = uv.KeyDown
	case tea.KeyRight:
		key.Code = uv.KeyRight
	case tea.KeyLeft:
		key.Code = uv.KeyLeft
	case tea.KeyHome:
		key.Code = uv.KeyHome
	case tea.KeyEnd:
		key.Code = uv.KeyEnd
	case tea.KeyPgUp:
		key.Code = uv.KeyPgUp
	case tea.KeyPgDown:
		key.Code = uv.KeyPgDown

	case tea.KeyShiftUp:
		key.Code, key.Mod = uv.KeyUp, key.Mod|vt.ModShift
	case tea.KeyShiftDown:
		key.Code, key.Mod = uv.KeyDown, key.Mod|vt.ModShift
	case tea.KeyShiftRight:
		key.Code, key.Mod = uv.KeyRight, key.Mod|vt.ModShift
	case tea.KeyShiftLeft:
		key.Code, key.Mod = uv.KeyLeft, key.Mod|vt.ModShift
	case tea.KeyCtrlUp:
		key.Code, key.Mod = uv.KeyUp, key.Mod|vt.ModCtrl
	case tea.KeyCtrlDown:
		key.Code, key.Mod = uv.KeyDown, key.Mod|vt.ModCtrl
	case tea.KeyCtrlRight:
		key.Code, key.Mod = uv.KeyRight, key.Mod|vt.ModCtrl
	case tea.KeyCtrlLeft:
		key.Code, key.Mod = uv.KeyLeft, key.Mod|vt.ModCtrl
	case tea.KeyCtrlHome:
		key.Code, key.Mod = uv.KeyHome, key.Mod|vt.ModCtrl
	case tea.KeyCtrlEnd:
		key.Code, key.Mod = uv.KeyEnd, key.Mod|vt.ModCtrl

	default:
		if fn, ok := functionKeys[msg.Type]; ok {
			key.Code = fn
			break
		}
		// The remaining Bubble Tea control-key constants are the ASCII control
		// codes themselves: 1 is ctrl+a, 26 is ctrl+z. Tab/enter/esc share those
		// values and are handled above.
		if code := int(msg.Type); code >= 1 && code <= 26 {
			key.Code = rune('a' + code - 1)
			key.Mod |= vt.ModCtrl
			break
		}
		return key, false
	}

	return key, true
}

var functionKeys = map[tea.KeyType]rune{
	tea.KeyF1:  uv.KeyF1,
	tea.KeyF2:  uv.KeyF2,
	tea.KeyF3:  uv.KeyF3,
	tea.KeyF4:  uv.KeyF4,
	tea.KeyF5:  uv.KeyF5,
	tea.KeyF6:  uv.KeyF6,
	tea.KeyF7:  uv.KeyF7,
	tea.KeyF8:  uv.KeyF8,
	tea.KeyF9:  uv.KeyF9,
	tea.KeyF10: uv.KeyF10,
	tea.KeyF11: uv.KeyF11,
	tea.KeyF12: uv.KeyF12,
	tea.KeyF13: uv.KeyF13,
	tea.KeyF14: uv.KeyF14,
	tea.KeyF15: uv.KeyF15,
	tea.KeyF16: uv.KeyF16,
	tea.KeyF17: uv.KeyF17,
	tea.KeyF18: uv.KeyF18,
	tea.KeyF19: uv.KeyF19,
	tea.KeyF20: uv.KeyF20,
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
