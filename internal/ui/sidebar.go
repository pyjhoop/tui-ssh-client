package ui

import (
	"fmt"

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
	// sessions is how many tabs are open on this server, so the list can say
	// which ones enter would switch to rather than dial.
	sessions int
}

func (i item) Title() string {
	if i.connect {
		return "+ Connect"
	}
	switch {
	case i.sessions > 1:
		return fmt.Sprintf("● %s (%d)", i.server.Title(), i.sessions)
	case i.sessions == 1:
		return "● " + i.server.Title()
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
	list    list.Model
	servers []model.Server
	// open counts sessions per server ID; the markers are rebuilt from it.
	open map[string]int
}

func newSidebar(servers []model.Server, width, height int) sidebar {
	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.
		Foreground(colorAccent).BorderForeground(colorAccent)
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.
		Foreground(colorAccentDim).BorderForeground(colorAccent)

	l := list.New(itemsFor(servers, nil), delegate, width, height)
	l.Title = "Servers"
	l.Styles.Title = l.Styles.Title.Background(colorAccent).Foreground(colorBg)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.DisableQuitKeybindings()

	return sidebar{list: l}
}

func itemsFor(servers []model.Server, open map[string]int) []list.Item {
	items := make([]list.Item, 0, len(servers)+1)
	items = append(items, item{connect: true})
	for _, s := range servers {
		items = append(items, item{server: s, sessions: open[s.ID]})
	}
	return items
}

// SetServers reloads the list, keeping the cursor in range.
func (s *sidebar) SetServers(servers []model.Server) {
	idx := s.list.Index()
	s.servers = servers
	s.list.SetItems(itemsFor(servers, s.open))
	if idx >= len(servers)+1 {
		idx = len(servers)
	}
	s.list.Select(idx)
}

// SetOpen redraws the session markers. The cursor stays where it was: this runs
// whenever a tab opens or closes, which must never move the selection.
func (s *sidebar) SetOpen(open map[string]int) {
	s.open = open
	idx := s.list.Index()
	s.list.SetItems(itemsFor(s.servers, open))
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
