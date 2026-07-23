package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pyjhoop/ssh-client/internal/model"
)

// formAction is what the root model should do after handing a key to the form.
type formAction int

const (
	formNone formAction = iota
	formSubmit
	formCancel
)

// Field indices. The auth toggle is a pseudo-field: it takes focus but has no
// text input behind it.
const (
	fieldName = iota
	fieldGroup
	fieldHost
	fieldPort
	fieldUser
	fieldAuth
	fieldSecret // password, or key path
	fieldKeyBody
	fieldCount
)

const keyBodyHeight = 6

// formHeaderRows is the number of rows above the first field. The panel's own
// title bar carries the heading, so the form starts straight in on its fields.
const formHeaderRows = 0

type form struct {
	inputs [fieldCount]textinput.Model
	keyPad textarea.Model

	// editingID is empty for a new entry and the server's ID when editing one.
	// It decides between Store.Add and Store.Update, and keeps keys/<id>.pem
	// pointing at the same file.
	editingID string
	// lastUsed is carried through untouched: Update replaces the whole entry, so
	// a field with no input behind it would be erased by every edit.
	lastUsed time.Time

	auth    model.AuthMethod
	focused int
	err     string
	width   int
	height  int
}

func newForm(width, height int) form {
	f := form{auth: model.AuthPassword, width: width, height: height}

	mk := func(placeholder string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.Prompt = "› "
		ti.CharLimit = 512
		return ti
	}

	f.inputs[fieldName] = mk("optional label")
	f.inputs[fieldGroup] = mk("optional folder")
	f.inputs[fieldHost] = mk("example.com")
	f.inputs[fieldPort] = mk(strconv.Itoa(model.DefaultPort))
	f.inputs[fieldUser] = mk("root")
	f.inputs[fieldAuth] = mk("")
	f.inputs[fieldSecret] = mk("")

	f.keyPad = textarea.New()
	f.keyPad.Placeholder = "paste a private key here to save it to keys/<id>.pem (0600)"
	f.keyPad.SetHeight(keyBodyHeight)
	f.keyPad.ShowLineNumbers = false

	f.applyAuth()
	f.setSize(width, height)
	f.focus(fieldName)
	return f
}

// newFormFor builds an edit form pre-filled from srv.
//
// The key body textarea stays empty even for key auth: we never read the stored
// pem back into the UI. Leaving it blank keeps the existing KeyPath; pasting a
// new key overwrites keys/<id>.pem, which is the same path because the ID does
// not change.
func newFormFor(srv model.Server, width, height int) form {
	f := newForm(width, height)
	f.editingID = srv.ID
	f.lastUsed = srv.LastUsed
	f.auth = srv.Auth
	if f.auth == "" {
		f.auth = model.AuthPassword
	}

	f.inputs[fieldName].SetValue(srv.Name)
	f.inputs[fieldGroup].SetValue(srv.Group)
	f.inputs[fieldHost].SetValue(srv.Host)
	port := srv.Port
	if port == 0 {
		port = model.DefaultPort
	}
	f.inputs[fieldPort].SetValue(strconv.Itoa(port))
	f.inputs[fieldUser].SetValue(srv.User)
	// The password comes from the vault, injected by the caller. Without it an
	// edit would blank the field and Update would replace the entry with one
	// that has no password at all.
	if f.auth == model.AuthPassword {
		f.inputs[fieldSecret].SetValue(srv.Password)
	} else {
		f.inputs[fieldSecret].SetValue(srv.KeyPath)
	}

	f.applyAuth()
	f.focus(fieldName)
	return f
}

// applyAuth updates the secret field for the current auth method.
func (f *form) applyAuth() {
	secret := &f.inputs[fieldSecret]
	if f.auth == model.AuthPassword {
		secret.Placeholder = "password · stored encrypted in vault.age"
		secret.EchoMode = textinput.EchoPassword
		secret.EchoCharacter = '•'
	} else {
		secret.Placeholder = "~/.ssh/id_ed25519 (leave empty to use the pasted key)"
		secret.EchoMode = textinput.EchoNormal
	}
}

func (f *form) setSize(width, height int) {
	f.width, f.height = width, height
	inner := maxInt(width-4, 10)
	for i := range f.inputs {
		f.inputs[i].Width = inner - 4
	}
	f.keyPad.SetWidth(inner)
}

// visible reports whether a field is shown for the current auth method.
//
// ssh-agent has no secret of its own: the agent holds the key and we hold
// nothing, so both the secret field and the key pad go away.
func (f *form) visible(idx int) bool {
	switch idx {
	case fieldKeyBody:
		return f.auth == model.AuthKey
	case fieldSecret:
		return f.auth != model.AuthAgent
	default:
		return true
	}
}

func (f *form) focus(idx int) {
	for i := range f.inputs {
		f.inputs[i].Blur()
	}
	f.keyPad.Blur()

	f.focused = idx
	switch {
	case idx == fieldKeyBody:
		f.keyPad.Focus()
	case idx == fieldAuth:
		// Toggle has no cursor of its own.
	default:
		f.inputs[idx].Focus()
	}
}

func (f *form) move(delta int) {
	idx := f.focused
	for range fieldCount {
		idx = (idx + delta + fieldCount) % fieldCount
		if f.visible(idx) {
			break
		}
	}
	f.focus(idx)
}

func (f *form) Update(msg tea.Msg) (tea.Cmd, formAction) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEsc:
			return nil, formCancel
		case tea.KeyTab, tea.KeyDown:
			if f.focused == fieldKeyBody && msg.Type == tea.KeyDown {
				break // let the textarea move its own cursor
			}
			f.move(1)
			return nil, formNone
		case tea.KeyShiftTab, tea.KeyUp:
			if f.focused == fieldKeyBody && msg.Type == tea.KeyUp {
				break
			}
			f.move(-1)
			return nil, formNone
		case tea.KeyCtrlS:
			return nil, formSubmit
		case tea.KeyEnter:
			// Enter inserts a newline inside the key textarea; everywhere else
			// it saves.
			if f.focused != fieldKeyBody {
				return nil, formSubmit
			}
		case tea.KeyLeft:
			if f.focused == fieldAuth {
				f.toggleAuth(-1)
				return nil, formNone
			}
		case tea.KeyRight, tea.KeySpace:
			if f.focused == fieldAuth {
				f.toggleAuth(1)
				return nil, formNone
			}
		}

	case tea.MouseMsg:
		if msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft {
			if idx, ok := f.fieldAtRow(msg.Y); ok {
				f.focus(idx)
			}
			return nil, formNone
		}
	}

	var cmd tea.Cmd
	if f.focused == fieldKeyBody {
		f.keyPad, cmd = f.keyPad.Update(msg)
	} else if f.focused != fieldAuth {
		f.inputs[f.focused], cmd = f.inputs[f.focused].Update(msg)
	}
	return cmd, formNone
}

// authOrder is the toggle's cycle. Agent is last because it is the one that
// stores nothing — the option to reach for once the other two feel like work.
var authOrder = []model.AuthMethod{model.AuthPassword, model.AuthKey, model.AuthAgent}

func (f *form) toggleAuth(delta int) {
	idx := 0
	for i, m := range authOrder {
		if m == f.auth {
			idx = i
			break
		}
	}
	f.auth = authOrder[(idx+delta+len(authOrder))%len(authOrder)]
	f.inputs[fieldSecret].SetValue("")
	f.applyAuth()
	// The secret field disappears for agent auth; do not leave focus on it.
	if !f.visible(f.focused) {
		f.move(1)
	}
}

// fieldBlock is the row range a field occupies, label and input included.
type fieldBlock struct {
	idx        int
	start, end int // [start, end)
}

// blocks lays the visible fields out vertically. The layout is deterministic,
// so this stays in sync with View without threading state through it.
func (f *form) blocks() []fieldBlock {
	out := make([]fieldBlock, 0, fieldCount)
	y := formHeaderRows
	for i := range fieldCount {
		if !f.visible(i) {
			continue
		}
		height := 2 // label + input
		if i == fieldKeyBody {
			height = 1 + keyBodyHeight
		}
		out = append(out, fieldBlock{idx: i, start: y, end: y + height})
		y += height
	}
	return out
}

// fieldAtRow resolves a click row to the field that owns it.
func (f *form) fieldAtRow(y int) (int, bool) {
	blocks := f.blocks()
	for _, b := range blocks {
		if y >= b.start && y < b.end {
			return b.idx, true
		}
	}
	if len(blocks) > 0 && y >= blocks[len(blocks)-1].end {
		return blocks[len(blocks)-1].idx, true
	}
	return 0, false
}

// setGroups shows the groups that already exist as the Group placeholder. It is
// a hint, not a completion: a one-line input does not get a popup.
func (f *form) setGroups(groups []string) {
	if len(groups) == 0 {
		return
	}
	f.inputs[fieldGroup].Placeholder = "optional folder · " + strings.Join(groups, ", ")
}

var fieldLabels = [fieldCount]string{
	fieldName:    "Name",
	fieldGroup:   "Group",
	fieldHost:    "Host",
	fieldPort:    "Port",
	fieldUser:    "User",
	fieldAuth:    "Auth",
	fieldSecret:  "Password",
	fieldKeyBody: "Key body",
}

func (f *form) View() string {
	var b strings.Builder

	for i := range fieldCount {
		if !f.visible(i) {
			continue
		}
		label := fieldLabels[i]
		if i == fieldSecret && f.auth == model.AuthKey {
			label = "Key path"
		}
		labelStyle := styleFormLabel
		if i == f.focused {
			labelStyle = styleFormLabelFocused
		}
		b.WriteString(labelStyle.Render(label))
		b.WriteString("\n")

		switch i {
		case fieldAuth:
			b.WriteString(f.authToggleView())
		case fieldKeyBody:
			b.WriteString(f.keyPad.View())
		default:
			b.WriteString(f.inputs[i].View())
		}
		b.WriteString("\n")
	}

	if f.err != "" {
		b.WriteString("\n")
		b.WriteString(styleError.Render("✗ " + f.err))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleHint.Render("tab/shift+tab move · ←/→ toggle auth · enter save · ctrl+s save · esc cancel"))

	return b.String()
}

func (f *form) authToggleView() string {
	opt := func(name string, on bool) string {
		if on {
			return styleToggleOn.Render(" " + name + " ")
		}
		return styleToggleOff.Render(" " + name + " ")
	}
	return "  " + lipgloss.JoinHorizontal(
		lipgloss.Top,
		opt("Password", f.auth == model.AuthPassword),
		" ",
		opt("Key", f.auth == model.AuthKey),
		" ",
		opt("Agent", f.auth == model.AuthAgent),
	)
}

// Server builds a Server from the current field values. The pasted key body is
// returned separately because it is a secret: since v6 it goes into the vault,
// not into keys/<id>.pem, and only the root model may put it there.
func (f *form) Server() (model.Server, string, error) {
	port := model.DefaultPort
	if raw := strings.TrimSpace(f.inputs[fieldPort].Value()); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return model.Server{}, "", fmt.Errorf("port must be a number: %q", raw)
		}
		port = p
	}

	srv := model.Server{
		ID:       f.editingID,
		Name:     strings.TrimSpace(f.inputs[fieldName].Value()),
		Group:    strings.TrimSpace(f.inputs[fieldGroup].Value()),
		Host:     strings.TrimSpace(f.inputs[fieldHost].Value()),
		Port:     port,
		User:     strings.TrimSpace(f.inputs[fieldUser].Value()),
		Auth:     f.auth,
		LastUsed: f.lastUsed,
	}

	keyBody := ""
	switch {
	case f.auth == model.AuthAgent:
		// Nothing to collect: the agent holds the credential.
	case f.auth == model.AuthPassword:
		srv.Password = f.inputs[fieldSecret].Value()
	default:
		srv.KeyPath = strings.TrimSpace(f.inputs[fieldSecret].Value())
		keyBody = strings.TrimSpace(f.keyPad.Value())
		if srv.KeyPath == "" && keyBody == "" {
			return model.Server{}, "", fmt.Errorf("provide a key path or paste a key")
		}
	}
	return srv, keyBody, nil
}
