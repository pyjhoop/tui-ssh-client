package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// helpState is the floating shortcut card. It holds no copy of the bindings: it
// draws the keymap, which is the whole point — a help screen with its own table
// of strings is a help screen that goes out of date.
type helpState struct {
	// ctx is where the card was opened from. That section is lifted to the top,
	// because the keys for the screen you are on are the ones you came for.
	ctx    Context
	off    int
	rows   int // body height of the last render, for paging
	total  int // rendered lines of the last render, for clamping
	search textinput.Model
	// searching is true while the query is being typed. The card keeps its
	// results after enter, the way the sidebar filter does.
	searching bool
	problems  []KeymapProblem
}

// helpAvailable reports whether ? would open the card right now.
//
// It is false wherever the key is not ours to take: inside a live session every
// key belongs to the remote shell, in a text field ? is a character, and while a
// dialog is up nothing may be stacked on top of it. The status line asks the
// same question before advertising the key — nothing on screen may name a key
// that would not work.
func (a *App) helpAvailable() bool {
	if a.help != nil || a.modalUp() {
		return false
	}
	// A frame too small to float a card in would swallow the keyboard and show
	// nothing for it. The status line stops offering the key at the same moment.
	if !a.helpFits() {
		return false
	}
	if a.focus == focusSidebar && a.sidebar.Filtering() {
		return false
	}
	switch a.focus {
	case focusSidebar, focusLocal, focusRemote:
		return true
	}
	return false
}

// modalUp reports whether a question is already on screen. It is the same set
// tabKey stands down for: the modal rule, asked as a predicate.
func (a *App) modalUp() bool {
	return a.unlock != nil || a.keyPass != nil || a.confirm != nil || a.pending != nil ||
		a.rename != nil || a.sftpErr != nil || a.importing != nil || a.syncForm != nil
}

// helpContext is the section the card opens on.
func (a *App) helpContext() Context {
	switch a.focus {
	case focusLocal, focusRemote:
		return ctxSFTP
	case focusImport:
		return ctxImport
	case focusSync:
		return ctxSync
	case focusForm:
		return ctxForm
	case focusSession:
		return ctxSession
	default:
		if a.rightMode == rightError {
			return ctxError
		}
		return ctxSidebar
	}
}

func (a *App) openHelp() {
	in := textinput.New()
	in.Prompt = "/ "
	in.CharLimit = 40
	a.help = &helpState{ctx: a.helpContext(), search: in, problems: a.keyProblems}
}

// handleHelpKey owns the keyboard while the card is up. Keys it does not know
// close it rather than falling through: nothing may reach the session behind it.
func (a *App) handleHelpKey(msg tea.KeyMsg) tea.Cmd {
	h := a.help
	if h == nil {
		return nil
	}

	if h.searching {
		switch msg.String() {
		case "esc":
			h.searching = false
			h.search.SetValue("")
			h.search.Blur()
			h.off = 0
			return nil
		case "enter":
			h.searching = false
			h.search.Blur()
			return nil
		}
		var cmd tea.Cmd
		h.search, cmd = h.search.Update(msg)
		h.off = 0
		return cmd
	}

	switch a.keys.Action(ctxHelp, msg.String()) {
	case actHelpSearch:
		h.searching = true
		h.search.Focus()
		h.off = 0
		return textinput.Blink
	case actHelpUp:
		h.scroll(-1)
	case actHelpDown:
		h.scroll(1)
	case actHelpPageUp:
		h.scroll(-maxInt(h.rows-1, 1))
	case actHelpPgDown:
		h.scroll(maxInt(h.rows-1, 1))
	case actHelpClose:
		a.closeHelp()
	default:
		// Any other key closes the card. It changed nothing while it was up, so
		// there is nothing to undo.
		if h.search.Value() != "" && msg.String() == "esc" {
			h.search.SetValue("")
			h.off = 0
			return nil
		}
		a.closeHelp()
	}
	return nil
}

// closeHelp puts the screen back exactly as it was: the card owns no state
// beyond its own scroll position.
func (a *App) closeHelp() { a.help = nil }

func (h *helpState) scroll(delta int) {
	h.off = clampInt(h.off+delta, 0, maxInt(h.total-h.rows, 0))
}

// helpMouse lets the wheel scroll the card and any click dismiss it, reporting
// whether it consumed the event. While the card is up nothing underneath may
// see the mouse either.
func (a *App) helpMouse(msg tea.MouseMsg) bool {
	h := a.help
	if h == nil {
		return false
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		h.scroll(-scrollStep)
	case tea.MouseButtonWheelDown:
		h.scroll(scrollStep)
	default:
		if msg.Action == tea.MouseActionPress {
			a.closeHelp()
		}
	}
	return true
}

// ── rendering ───────────────────────────────────────────────────────────────

const (
	// helpMaxCols keeps the card readable on a wide terminal: a shortcut list
	// stretched over 200 columns is two words and a desert.
	helpMaxCols = 110
	// helpTwoColCols is where the rows start pairing up. Below it a single
	// column is the only thing that fits without truncating every description,
	// and a truncated description is worse than a scroll.
	helpTwoColCols = 100
	helpColGap     = 3
)

// helpModal builds the card and where to put it. It reuses the split view's
// modal box: this is the first use of overlay outside SFTP mode, and it is the
// same compositor, on purpose — the ANSI width rules live in exactly one place.
func (a *App) helpModal() (box string, x, y int) {
	if a.help == nil {
		return "", 0, 0
	}
	maxCols := maxInt(a.width-2*borderSize-2*modalPadX-2, modalMinCols)
	cols := minInt(helpMaxCols, maxCols)
	rows := maxInt(a.panelHeight()-2-2*modalPadY, 3)
	return modalBox(a, a.helpView(cols, rows))
}

// helpFits reports whether the frame is big enough to float a card at all. On a
// very small terminal the box would be wider than the frame and overlay would
// skip its rows, leaving a broken half-card behind.
func (a *App) helpFits() bool {
	return a.width >= modalMinCols+2*modalPadX+2*borderSize && a.panelHeight() >= 8
}

// helpView renders the card body to exactly cols by at most rows lines.
func (a *App) helpView(cols, rows int) string {
	h := a.help
	query := strings.ToLower(strings.TrimSpace(h.search.Value()))

	var head []string
	title := "Keyboard shortcuts"
	if query != "" || h.searching {
		title = "Keyboard shortcuts — search"
	}
	head = append(head, styleFormTitle.Render(title))
	if h.searching || query != "" {
		head = append(head, h.searchLine())
	}
	head = append(head, "")

	foot := a.helpFooter(cols)

	// The body gets whatever the title and footer leave.
	bodyRows := maxInt(rows-len(head)-len(foot), 1)
	body := a.helpBody(cols, query)

	h.total = len(body)
	h.rows = bodyRows
	h.off = clampInt(h.off, 0, maxInt(len(body)-bodyRows, 0))

	view := body
	if len(view) > bodyRows {
		view = view[h.off : h.off+bodyRows]
	}

	// Built again now that the scroll figures are known. The footer's height
	// does not depend on them, so the budget above still holds — only the
	// sentence changes, and a card that is cut must say so.
	foot = a.helpFooter(cols)

	lines := make([]string, 0, len(head)+len(view)+len(foot))
	lines = append(lines, head...)
	lines = append(lines, view...)
	lines = append(lines, foot...)

	for i, l := range lines {
		lines[i] = padLine(l, cols)
	}
	return strings.Join(lines, "\n")
}

func (h *helpState) searchLine() string {
	if h.searching {
		return h.search.View()
	}
	return styleHint.Render("/ " + h.search.Value() + "   (enter kept these results)")
}

// helpFooter says where the keymap file is and, when the card is scrollable,
// how to move. It also carries any complaint about keys.json: the problems are
// reported on the status line once, and this is where they stay readable.
func (a *App) helpFooter(cols int) []string {
	h := a.help
	foot := []string{""}

	if len(h.problems) > 0 {
		foot = append(foot, styleError.Render(fmt.Sprintf("✗ %s in keys.json:", plural(len(h.problems), "problem"))))
		for _, p := range h.problems {
			foot = append(foot, styleError.Render("  "+p.String()))
		}
	}

	hint := "esc close · / search"
	if h.total > h.rows {
		hint = fmt.Sprintf("↑/↓ scroll (%d/%d) · %s", h.off+1, maxInt(h.total-h.rows+1, 1), hint)
	}
	foot = append(foot, styleHint.Render(hint))
	foot = append(foot, styleHint.Render("keys: "+a.store.KeysPath()))
	return foot
}

// helpBody lays out the sections, current context first. A search flattens
// everything into one list instead: the rule is v5's, a single substring over
// the whole set, in declaration order — a ranked search would reshuffle the
// rows and make the card a different shape every keystroke.
func (a *App) helpBody(cols int, query string) []string {
	var out []string

	if query != "" {
		rows := make([]string, 0, 16)
		for _, ctx := range contextOrder {
			for _, b := range a.keys.Bindings(ctx) {
				if !helpMatches(b, ctx, query) {
					continue
				}
				rows = append(rows, helpRow(b, cols, helpKeyColMax, contextTitles[ctx]))
			}
		}
		if len(rows) == 0 {
			return []string{styleHint.Render("  nothing matches " + query)}
		}
		return rows
	}

	order := append([]Context{a.help.ctx}, contextOrder...)
	seen := map[Context]bool{}
	for _, ctx := range order {
		if seen[ctx] {
			continue
		}
		seen[ctx] = true
		list := a.keys.Bindings(ctx)
		if len(list) == 0 {
			continue
		}

		title := contextTitles[ctx]
		if ctx == a.help.ctx {
			title += "  — you are here"
		}
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, styleGroupHeader.Render(title))

		rows := make([]string, 0, len(list))
		colWidth := cols
		if cols >= helpTwoColCols {
			colWidth = (cols - helpColGap) / 2
		}
		keyw := keyColumn(list)
		for _, b := range list {
			rows = append(rows, helpRow(b, colWidth, keyw, ""))
		}
		if cols >= helpTwoColCols {
			rows = pairRows(rows, colWidth, cols)
		}
		out = append(out, rows...)
	}
	return out
}

func helpMatches(b Binding, ctx Context, query string) bool {
	hay := strings.ToLower(strings.Join(b.Keys, " ") + " " + b.Label + " " + b.Desc + " " + string(b.Action) + " " + contextTitles[ctx])
	return strings.Contains(hay, query)
}

// helpKeyColMax caps the key column. Past it a long list of alternatives would
// eat the description, which is the half that cannot be guessed from the screen.
const helpKeyColMax = 16

// keyColumn sizes the key column to the section it is for: most sections need
// six columns, and only the tab bindings need all sixteen.
func keyColumn(list []Binding) int {
	w := 5
	for _, b := range list {
		w = maxInt(w, ansi.StringWidth(b.KeyList()))
	}
	return minInt(w, helpKeyColMax) + 1
}

// helpRow renders one binding. The widths are worked out on the plain strings
// and the styling goes on afterwards, because a style makes ansi.StringWidth
// the only way to measure and there is no reason to pay for that here.
func helpRow(b Binding, width, keyw int, badge string) string {
	keys := b.KeyList()
	if ansi.StringWidth(keys) > keyw-1 {
		keys = ansi.Truncate(keys, keyw-1, "…")
	}
	desc := b.Desc
	if badge != "" {
		desc += "  (" + badge + ")"
	}

	room := width - keyw - 2
	if room < 4 {
		// No room for a description: the key is the part that cannot be guessed.
		return "  " + styleFormLabelFocused.Render(padLine(keys, maxInt(width-2, 1)))
	}
	if ansi.StringWidth(desc) > room {
		desc = ansi.Truncate(desc, room, "…")
	}
	return "  " + styleFormLabelFocused.Render(padLine(keys, keyw)) + styleRowDetail.Render(desc)
}

// pairRows folds a single column of rows into two, column-major, so a long
// section reads top-to-bottom on the left and continues on the right.
func pairRows(rows []string, colWidth, total int) []string {
	if len(rows) < 2 {
		return rows
	}
	half := (len(rows) + 1) / 2
	out := make([]string, 0, half)
	gap := strings.Repeat(" ", total-2*colWidth)
	for i := range half {
		left := padLine(rows[i], colWidth)
		right := ""
		if i+half < len(rows) {
			right = rows[i+half]
		}
		out = append(out, padLine(left+gap+right, total))
	}
	return out
}
