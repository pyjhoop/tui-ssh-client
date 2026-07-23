package ui

import (
	"fmt"
	"strings"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/model"
)

// importRow is one line of the preview: a parsed block plus what we decided
// about it.
type importRow struct {
	entry  config.SSHConfigEntry
	marked bool
	dup    bool // an entry with the same user@host:port is already saved
}

// importer is the ~/.ssh/config preview. Import never runs on its own — it is
// opened with i, chosen with space, and only then does anything reach disk.
type importer struct {
	rows   []importRow
	cursor int
	path   string
}

// newImporter builds the preview, marking as duplicates the blocks that would
// land on a server we already have. Duplicates start unchecked, as does
// anything we refuse to interpret.
func newImporter(path string, entries []config.SSHConfigEntry, existing []model.Server) importer {
	have := make(map[string]bool, len(existing))
	for _, s := range existing {
		have[dialKey(s)] = true
	}

	rows := make([]importRow, 0, len(entries))
	for _, e := range entries {
		r := importRow{entry: e}
		if !e.Skip {
			r.dup = have[dialKey(e.Server())]
			r.marked = !r.dup
		}
		rows = append(rows, r)
	}
	return importer{rows: rows, path: path}
}

// dialKey identifies a server by what it actually connects to, which is what
// makes two entries the same host regardless of the label on them.
func dialKey(s model.Server) string {
	return strings.ToLower(s.User + "@" + s.Addr())
}

// importable reports whether a row may be selected at all.
func (r importRow) importable() bool { return !r.entry.Skip }

func (im *importer) move(delta int) {
	if len(im.rows) == 0 {
		return
	}
	im.cursor = clampInt(im.cursor+delta, 0, len(im.rows)-1)
}

// toggle flips the row under the cursor. Skipped blocks cannot be selected —
// there is nothing to import for them.
func (im *importer) toggle() {
	if im.cursor >= len(im.rows) || !im.rows[im.cursor].importable() {
		return
	}
	im.rows[im.cursor].marked = !im.rows[im.cursor].marked
}

// toggleAll selects everything importable, or clears the selection when
// anything is already selected.
func (im *importer) toggleAll() {
	any := false
	for _, r := range im.rows {
		if r.marked {
			any = true
			break
		}
	}
	for i := range im.rows {
		im.rows[i].marked = im.rows[i].importable() && !any
	}
}

// selected is the servers the user chose, ready to be appended.
func (im *importer) selected() []model.Server {
	var out []model.Server
	for _, r := range im.rows {
		if r.marked && r.importable() {
			out = append(out, r.entry.Server())
		}
	}
	return out
}

func (im *importer) skipped() int {
	n := 0
	for _, r := range im.rows {
		if !r.importable() {
			n++
		}
	}
	return n
}

// View renders one row per entry, the same shape as filePane and the sidebar.
func (im *importer) View(cols, rows int) string {
	if len(im.rows) == 0 {
		return fmt.Sprintf("No Host blocks in %s.", im.path)
	}

	out := make([]string, 0, rows)
	out = append(out,
		styleHint.Render(fmt.Sprintf("%s · %d blocks", im.path, len(im.rows))),
		"",
	)

	// The header lines above cost rows the list cannot use.
	body := maxInt(rows-len(out)-2, 1)
	offset := 0
	if im.cursor >= body {
		offset = im.cursor - body + 1
	}
	for i := offset; i < len(im.rows) && i-offset < body; i++ {
		out = append(out, im.renderRow(i, cols))
	}

	out = append(out, "", styleHint.Render(
		"space select · a all/none · enter import · esc cancel"))
	return strings.Join(out, "\n")
}

func (im *importer) renderRow(idx, cols int) string {
	r := im.rows[idx]

	box := "[ ]"
	switch {
	case !r.importable():
		box = "[-]"
	case r.marked:
		box = "[x]"
	}

	name := r.entry.Alias
	if name == "" {
		name = "(unnamed)"
	}

	// head is the part that is never dimmed; tail carries its own styling, so
	// the cursor row is rebuilt from the plain text instead (a highlight over
	// nested resets loses its background half way along the line).
	head := fmt.Sprintf("%s %s", box, name)
	var tail []string
	if !r.importable() {
		tail = append(tail, r.entry.Reason)
	} else {
		srv := r.entry.Server()
		detail := srv.Addr()
		if srv.User != "" {
			detail = srv.User + "@" + detail
		}
		tail = append(tail, detail)
		if r.entry.Identity != "" {
			tail = append(tail, r.entry.Identity)
		}
		if r.dup {
			tail = append(tail, "dup")
		}
	}

	if idx == im.cursor {
		return styleRowCursor.Render(padLine(head+"  "+strings.Join(tail, "  "), cols))
	}

	line := head
	for i, part := range tail {
		switch {
		case !r.importable():
			line += "  " + styleSkipped.Render(part)
		case part == "dup" && i == len(tail)-1 && r.dup:
			line += "  " + styleDupBadge.Render(part)
		default:
			line += "  " + styleRowDetail.Render(part)
		}
	}
	return padLine(line, cols)
}
