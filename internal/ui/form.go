package ui

import (
	"fmt"
	"strconv"
	"strings"

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

// applyAuth updates the secret field for the current auth method.
func (f *form) applyAuth() {
	secret := &f.inputs[fieldSecret]
	if f.auth == model.AuthPassword {
		secret.Placeholder = "password"
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
func (f *form) visible(idx int) bool {
	switch idx {
	case fieldKeyBody:
		return f.auth == model.AuthKey
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
		case tea.KeyLeft, tea.KeyRight, tea.KeySpace:
			if f.focused == fieldAuth {
				f.toggleAuth()
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

func (f *form) toggleAuth() {
	if f.auth == model.AuthPassword {
		f.auth = model.AuthKey
	} else {
		f.auth = model.AuthPassword
	}
	f.inputs[fieldSecret].SetValue("")
	f.applyAuth()
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

var fieldLabels = [fieldCount]string{
	fieldName:    "Name",
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
	)
}

// Server builds a Server from the current field values. The pasted key body is
// returned separately because writing it to disk belongs to the config package.
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
		Name: strings.TrimSpace(f.inputs[fieldName].Value()),
		Host: strings.TrimSpace(f.inputs[fieldHost].Value()),
		Port: port,
		User: strings.TrimSpace(f.inputs[fieldUser].Value()),
		Auth: f.auth,
	}

	keyBody := ""
	if f.auth == model.AuthPassword {
		srv.Password = f.inputs[fieldSecret].Value()
	} else {
		srv.KeyPath = strings.TrimSpace(f.inputs[fieldSecret].Value())
		keyBody = strings.TrimSpace(f.keyPad.Value())
		if srv.KeyPath == "" && keyBody == "" {
			return model.Server{}, "", fmt.Errorf("provide a key path or paste a key")
		}
	}
	return srv, keyBody, nil
}
