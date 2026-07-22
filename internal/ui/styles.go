package ui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent    = lipgloss.Color("39")
	colorAccentDim = lipgloss.Color("31")
	colorBg        = lipgloss.Color("235")
	colorMuted     = lipgloss.Color("244")
	colorErr       = lipgloss.Color("203")
	colorWarn      = lipgloss.Color("214")
)

var (
	stylePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorMuted)

	stylePanelFocused = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorAccent)

	// styleTitleBar matches the sidebar list's own heading so both panels read
	// as the same kind of thing.
	styleTitleBar = lipgloss.NewStyle().
			Background(colorAccent).Foreground(colorBg).Bold(true).Padding(0, 1)

	styleTitleDetail = lipgloss.NewStyle().Foreground(colorMuted)

	// styleTabIdle is a background session in the tab strip. The active one
	// reuses styleTitleBar, so a single tab looks exactly like the title bar did
	// before there were tabs at all.
	styleTabIdle = lipgloss.NewStyle().Foreground(colorMuted).Padding(0, 1)

	styleFormTitle = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	styleFormLabel = lipgloss.NewStyle().Foreground(colorMuted)

	styleFormLabelFocused = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	styleToggleOn = lipgloss.NewStyle().
			Background(colorAccent).Foreground(colorBg).Bold(true)

	styleToggleOff = lipgloss.NewStyle().Foreground(colorMuted)

	styleError = lipgloss.NewStyle().Foreground(colorErr)

	styleWarning = lipgloss.NewStyle().Foreground(colorWarn)

	styleHint = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	styleStatus = lipgloss.NewStyle().Foreground(colorMuted)

	// styleRowCursor is the highlighted row in a file pane; styleRowDragged marks
	// the file that is currently being dragged out of it.
	styleRowCursor = lipgloss.NewStyle().
			Background(colorAccent).Foreground(colorBg).Bold(true)

	styleRowDragged = lipgloss.NewStyle().Foreground(colorWarn)

	// styleRowMarked colours a multi-selected row. The cursor style wins when a
	// row is both, so the marker glyph is what still distinguishes it.
	styleRowMarked = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	// The progress bar is drawn by hand rather than with bubbles/progress: its
	// width is part of the layout arithmetic, so nothing else may decide it.
	styleBarFill  = lipgloss.NewStyle().Foreground(colorAccent)
	styleBarEmpty = lipgloss.NewStyle().Foreground(colorAccentDim)

	// styleModal is the floating dialog in the split view. Its border is the
	// accent colour so it reads as being on top of the panes, not one of them.
	styleModal = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent)
)

// dropStyle highlights a pane that is a valid drop target while a drag is in
// flight, so the border says where the file would land.
func dropStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorWarn)
}

// panelStyle picks the border colour for a panel based on focus.
func panelStyle(focused bool) lipgloss.Style {
	if focused {
		return stylePanelFocused
	}
	return stylePanel
}
