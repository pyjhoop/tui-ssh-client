package ui

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/pyjhoop/tui-ssh-client/internal/model"
)

// sidebarWidth is the fixed outer width of the left panel.
const sidebarWidth = 30

// item is one row in the sidebar: the "+ Connect" action, a group header, or a
// server.
type item struct {
	connect bool
	header  bool

	// group is the header's own name, or the server's membership.
	group     string
	collapsed bool
	count     int // header only: how many servers are inside
	// hasOpen marks a collapsed group that still has a live session inside, so
	// folding a group never hides the fact that it is connected.
	hasOpen bool

	server model.Server
	// sessions is how many tabs are open on this server, so the list can say
	// which ones enter would switch to rather than dial.
	sessions int
}

func (i item) Title() string {
	switch {
	case i.connect:
		return "+ Connect"
	case i.header:
		arrow := "▾"
		if i.collapsed {
			arrow = "▸"
		}
		marker := ""
		if i.collapsed && i.hasOpen {
			marker = " ●"
		}
		return fmt.Sprintf("%s %s (%d)%s", arrow, i.group, i.count, marker)
	case i.sessions > 1:
		return fmt.Sprintf("● %s (%d)", i.server.Title(), i.sessions)
	case i.sessions == 1:
		return "● " + i.server.Title()
	}
	return i.server.Title()
}

// Detail is the dim right-hand half of a server row. Headers and the connect
// action have none: their titles already say everything.
func (i item) Detail() string {
	if i.connect || i.header {
		return ""
	}
	return i.server.UserHost()
}

func (i item) Description() string {
	if i.connect {
		return "register a new server"
	}
	if i.header {
		return ""
	}
	return i.server.Description()
}

// FilterValue is the haystack containsFilter matches against.
//
// Group headers deliberately answer with a byte no typed term can contain, so
// they drop out of every non-empty search: a filtered list is a flat list of
// results, and a header floating in it would only blur what "result" means.
func (i item) FilterValue() string {
	switch {
	case i.header:
		return "\x00"
	case i.connect:
		return i.Title()
	}
	return i.server.FilterKey()
}

// rowDelegate draws one line per entry. bubbles' default delegate is three rows
// tall, which leaves no room for group headers (Height() is per-delegate, not
// per-item) and spends two thirds of the sidebar on blank space. filePane
// already renders one row per entry; this is the same shape.
type rowDelegate struct{}

func (rowDelegate) Height() int                         { return 1 }
func (rowDelegate) Spacing() int                        { return 0 }
func (rowDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (d rowDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it, ok := listItem.(item)
	if !ok {
		return
	}
	cols := maxInt(m.Width(), 1)
	title := " " + it.Title()
	detail := it.Detail()

	// The width arithmetic happens before any styling: ANSI counted as text is
	// how rows end up short. The detail is the first thing dropped when the
	// sidebar is narrow — the name is what identifies the row.
	left := cols - len([]rune(detail)) - 1
	if detail != "" && left < len([]rune(title))+2 {
		detail, left = "", cols
	}

	switch {
	case index == m.Index():
		line := padLine(title, left) + detail
		fmt.Fprint(w, styleRowCursor.Render(padLine(line, cols)))
	case it.header:
		fmt.Fprint(w, styleGroupHeader.Render(padLine(title, cols)))
	case detail != "":
		fmt.Fprint(w, padLine(title, left)+styleRowDetail.Render(detail+" "))
	default:
		fmt.Fprint(w, padLine(title, cols))
	}
}

// sidebar is the server list on the left.
type sidebar struct {
	list    list.Model
	servers []model.Server
	// open counts sessions per server ID; the markers are rebuilt from it.
	open map[string]int
	// collapsed is the set of folded group names, mirrored to ui.json.
	collapsed map[string]bool
	// sortRecent orders servers inside each group by last connection.
	sortRecent bool
}

func newSidebar(servers []model.Server, width, height int) sidebar {
	s := sidebar{collapsed: map[string]bool{}}

	l := list.New(nil, rowDelegate{}, width, height)
	l.Title = "Servers"
	l.Styles.Title = l.Styles.Title.Background(colorAccent).Foreground(colorBg)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()

	// Filtering is on from v5. It is safe now only because the app hands every
	// key to the list while FilterState is not Unfiltered — otherwise n/e/d/f
	// would be swallowed as filter text, which is why v0 turned it off.
	l.SetFilteringEnabled(true)
	l.Filter = containsFilter
	l.FilterInput.Prompt = "/ "

	s.list = l
	s.servers = servers
	s.reload()
	return s
}

// containsFilter is a plain substring match over FilterValue, preserving the
// original order. bubbles' default ranks by a Levenshtein-ish score, which
// reorders the list and floats "podman-host" above the actual prod boxes.
func containsFilter(term string, targets []string) []list.Rank {
	term = strings.ToLower(strings.TrimSpace(term))
	ranks := make([]list.Rank, 0, len(targets))
	for i, t := range targets {
		if term == "" || strings.Contains(strings.ToLower(t), term) {
			ranks = append(ranks, list.Rank{Index: i, MatchedIndexes: nil})
		}
	}
	return ranks
}

// itemsFor flattens servers into list rows: the connect action, the ungrouped
// servers, then one header per group followed by its members.
//
// A collapsed group's servers are left out of the slice entirely rather than
// hidden at render time — the list's cursor and paging arithmetic counts items,
// so a row that exists but does not draw would put the cursor on nothing.
//
// With no groups at all this produces exactly the v4 list, header-free.
func itemsFor(servers []model.Server, open map[string]int, collapsed map[string]bool, sortRecent bool) []list.Item {
	items := make([]list.Item, 0, len(servers)+1)
	items = append(items, item{connect: true})

	var (
		loose  []model.Server
		groups []string
		byName = map[string][]model.Server{}
	)
	for _, s := range servers {
		g := strings.TrimSpace(s.Group)
		if g == "" {
			loose = append(loose, s)
			continue
		}
		if _, seen := byName[g]; !seen {
			groups = append(groups, g)
		}
		byName[g] = append(byName[g], s)
	}
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i]) < strings.ToLower(groups[j])
	})

	appendServers := func(list_ []model.Server) {
		sortServers(list_, sortRecent)
		for _, s := range list_ {
			items = append(items, item{server: s, group: s.Group, sessions: open[s.ID]})
		}
	}
	appendServers(loose)

	for _, g := range groups {
		members := byName[g]
		folded := collapsed[g]
		hasOpen := false
		for _, s := range members {
			if open[s.ID] > 0 {
				hasOpen = true
				break
			}
		}
		items = append(items, item{
			header:    true,
			group:     g,
			collapsed: folded,
			count:     len(members),
			hasOpen:   hasOpen,
		})
		if !folded {
			appendServers(members)
		}
	}
	return items
}

// sortServers orders one group in place: most recently used first when the
// option is on, otherwise the order the file has them in.
func sortServers(servers []model.Server, recent bool) {
	if !recent {
		return
	}
	sort.SliceStable(servers, func(i, j int) bool {
		return servers[i].LastUsed.After(servers[j].LastUsed)
	})
}

// reload rebuilds the rows from the current servers, markers and fold state,
// keeping the cursor in range.
func (s *sidebar) reload() {
	idx := s.list.Index()
	items := itemsFor(s.servers, s.open, s.collapsed, s.sortRecent)
	s.list.SetItems(items)
	if idx >= len(items) {
		idx = maxInt(len(items)-1, 0)
	}
	s.list.Select(idx)
}

// SetServers reloads the list, keeping the cursor in range.
func (s *sidebar) SetServers(servers []model.Server) {
	s.servers = servers
	s.reload()
}

// SetOpen redraws the session markers. The cursor stays where it was: this runs
// whenever a tab opens or closes, which must never move the selection.
func (s *sidebar) SetOpen(open map[string]int) {
	s.open = open
	s.reload()
}

// SetCollapsed replaces the fold state, e.g. from ui.json at startup.
func (s *sidebar) SetCollapsed(groups []string) {
	s.collapsed = make(map[string]bool, len(groups))
	for _, g := range groups {
		s.collapsed[g] = true
	}
	s.reload()
}

// CollapsedGroups is the fold state in the form ui.json stores, name-sorted so
// the file does not churn between saves.
func (s *sidebar) CollapsedGroups() []string {
	out := make([]string, 0, len(s.collapsed))
	for g, on := range s.collapsed {
		if on {
			out = append(out, g)
		}
	}
	sort.Strings(out)
	return out
}

// ToggleGroup folds or unfolds the highlighted header, reporting whether the
// cursor was actually on one.
func (s *sidebar) ToggleGroup() bool {
	it, ok := s.Selected()
	if !ok || !it.header {
		return false
	}
	s.collapsed[it.group] = !s.collapsed[it.group]
	s.reload()
	return true
}

// SetGroupCollapsed folds or unfolds the highlighted header explicitly, for the
// ←/→ bindings where the direction means something.
func (s *sidebar) SetGroupCollapsed(fold bool) bool {
	it, ok := s.Selected()
	if !ok || !it.header {
		return false
	}
	if s.collapsed[it.group] == fold {
		return false
	}
	s.collapsed[it.group] = fold
	s.reload()
	return true
}

// SetSortRecent switches between file order and most-recently-used.
func (s *sidebar) SetSortRecent(on bool) {
	s.sortRecent = on
	s.reload()
}

func (s *sidebar) SortRecent() bool { return s.sortRecent }

// Groups lists the existing group names, for the form's placeholder.
func (s *sidebar) Groups() []string {
	seen := map[string]bool{}
	var out []string
	for _, srv := range s.servers {
		g := strings.TrimSpace(srv.Group)
		if g != "" && !seen[g] {
			seen[g] = true
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

// Filtering reports whether the list owns the keyboard: while the filter is
// being typed *or* applied, single-letter shortcuts belong to it, not to us.
func (s *sidebar) Filtering() bool {
	return s.list.FilterState() == list.Filtering
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
