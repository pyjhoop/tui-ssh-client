package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// confirm is the shared yes/no panel: host key approval and server deletion use
// it in v1, and v2's transfer alerts will reuse it.
//
// It replaces the right panel's body rather than floating over it. lipgloss v1
// has no safe overlay compositor, and splicing rows that already carry ANSI
// styling breaks the width arithmetic every layout invariant depends on — so
// area replacement is the only way to keep the frame rectangular.
type confirm struct {
	title  string
	lines  []string
	warn   string // rendered in the warning colour when non-empty
	accept string // label for the accepting key, e.g. "[enter] delete"
	onYes  tea.Cmd
	onNo   tea.Cmd
}

// resolve answers the dialog. It reports the command to run and whether the key
// was one the dialog handles; anything else is swallowed so a stray keystroke
// cannot leak into the session behind it.
func (c *confirm) resolve(keys *Keymap, msg tea.KeyMsg) (tea.Cmd, bool) {
	switch keys.Action(ctxConfirm, msg.String()) {
	case actConfirmYes:
		return c.onYes, true
	case actConfirmNo:
		return c.onNo, true
	}
	return nil, false
}

func (c *confirm) View() string {
	var b strings.Builder
	b.WriteString(styleFormTitle.Render(c.title))
	b.WriteString("\n\n")
	for _, l := range c.lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if c.warn != "" {
		b.WriteString("\n")
		b.WriteString(styleWarning.Render(c.warn))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	accept := c.accept
	if accept == "" {
		accept = "[enter] confirm"
	}
	b.WriteString(styleHint.Render(accept + "   [esc] cancel"))
	return b.String()
}
