package ui

import (
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/pyjhoop/ssh-client/internal/model"
)

// sidebarWidth is the fixed outer width of the left panel.
const sidebarWidth = 30

// item is one row in the sidebar: either the "+ Connect" action or a server.
type item struct {
	connect bool
	server  model.Server
}

func (i item) Title() string {
	if i.connect {
		return "+ Connect"
	}
	return i.server.Title()
}

func (i item) Description() string {
	if i.connect {
		return "register a new server"
	}
	return i.server.Description()
}

func (i item) FilterValue() string { return i.Title() }

// sidebar is the server list on the left.
type sidebar struct {
	list list.Model
}

func newSidebar(servers []model.Server, width, height int) sidebar {
	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(colorAccent).BorderForeground(colorAccent)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(colorAccentDim).BorderForeground(colorAccent)

	l := list.New(itemsFor(servers), delegate, width, height)
	l.Title = "Servers"
	l.Styles.Title = l.Styles.Title.Background(colorAccent).Foreground(colorBg)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()

	return sidebar{list: l}
}

func itemsFor(servers []model.Server) []list.Item {
	items := make([]list.Item, 0, len(servers)+1)
	items = append(items, item{connect: true})
	for _, s := range servers {
		items = append(items, item{server: s})
	}
	return items
}

// SetServers reloads the list, keeping the cursor in range.
func (s *sidebar) SetServers(servers []model.Server) {
	idx := s.list.Index()
	s.list.SetItems(itemsFor(servers))
	if idx >= len(servers)+1 {
		idx = len(servers)
	}
	s.list.Select(idx)
}

func (s *sidebar) SetSize(width, height int) {
	s.list.SetSize(width, height)
}

func (s *sidebar) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	s.list, cmd = s.list.Update(msg)
	return cmd
}

// Selected returns the highlighted row.
func (s *sidebar) Selected() (item, bool) {
	it, ok := s.list.SelectedItem().(item)
	return it, ok
}

func (s *sidebar) View() string {
	return s.list.View()
}
