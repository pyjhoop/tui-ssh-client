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
