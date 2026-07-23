package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
	"github.com/pyjhoop/ssh-client/internal/vault"
)

// maxUnlockAttempts is how many wrong passphrases we sit through before
// quitting. There is no point holding an infinite retry loop open: anyone
// running a real brute force takes the file away and does it offline, so all
// this bound costs an attacker is nothing and it costs us a stuck app.
const maxUnlockAttempts = 3

// unlockMode is which question the screen is asking.
type unlockMode int

const (
	unlockOpen   unlockMode = iota // there is a vault; type the passphrase
	unlockCreate                   // there is none yet; choose one, twice
)

// unlockState is the startup gate. It is the strongest form of the modal rule
// the rest of the app follows: while it is up nothing else is drawn at all, and
// every key and every mouse event belongs to it.
type unlockState struct {
	mode    unlockMode
	input   textinput.Model
	confirm textinput.Model // second field, create mode only
	second  bool            // create mode: the confirmation has focus
	attempt int
	busy    bool // a decrypt is in flight; scrypt takes a while
	err     string
	// why explains what the passphrase is for when it is not simply "unlock at
	// startup" — migrating plaintext, or saving the first secret.
	why string
	// plaintext is the pre-v6 config found at startup, migrated as soon as the
	// vault is open. It is empty for an ordinary unlock.
	plaintext config.Plaintext
	// after runs once the vault is open, for the flows that needed it before
	// they could finish (saving the first password, registering sync).
	after func(*App) tea.Cmd
}

func newUnlock(mode unlockMode, why string) *unlockState {
	mk := func(placeholder string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.Prompt = "› "
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
		ti.CharLimit = 256
		ti.Width = 40
		return ti
	}
	u := &unlockState{mode: mode, why: why}
	u.input = mk("passphrase")
	u.confirm = mk("again")
	u.input.Focus()
	return u
}

// ── messages and commands ───────────────────────────────────────────────────

// startupScanMsg is what the config directory looks like before anything is
// drawn: whether there is a vault to open, and whether there are pre-v6
// secrets sitting in the clear.
type startupScanMsg struct {
	hasVault  bool
	plaintext config.Plaintext
	err       error
}

// unlockedMsg is the answer to one passphrase attempt. Decryption is scrypt at
// a deliberately high work factor, so it never runs inside Update.
type unlockedMsg struct {
	pass    string
	secrets vault.Secrets
	report  config.MigrateReport
	err     error
}

// secretsSavedMsg reports a vault write. Only a failure is worth saying
// anything about — a successful one is invisible by design.
type secretsSavedMsg struct {
	status string
	err    error
}

func scanStartup(store *config.Store) tea.Cmd {
	return func() tea.Msg {
		has := store.HasVault()
		pt, err := store.ScanPlaintext()
		return startupScanMsg{hasVault: has, plaintext: pt, err: err}
	}
}

// unlockCmd decrypts, and migrates in the same command when there is plaintext
// to move. Both are slow and neither may block the UI goroutine.
func unlockCmd(store *config.Store, pass string, pt config.Plaintext) tea.Cmd {
	return func() tea.Msg {
		sec, err := store.LoadSecrets(pass)
		if err != nil {
			return unlockedMsg{err: err}
		}
		var rep config.MigrateReport
		if pt.Any() {
			rep, err = store.Migrate(pt, &sec, pass)
			if err != nil {
				return unlockedMsg{err: err}
			}
		}
		return unlockedMsg{pass: pass, secrets: sec, report: rep}
	}
}

// createVaultCmd seals a brand new vault under a freshly chosen passphrase.
func createVaultCmd(store *config.Store, pass string, pt config.Plaintext) tea.Cmd {
	return func() tea.Msg {
		sec := vault.Secrets{Version: vault.CurrentVersion}
		var rep config.MigrateReport
		if pt.Any() {
			var err error
			rep, err = store.Migrate(pt, &sec, pass)
			if err != nil {
				return unlockedMsg{err: err}
			}
		} else if err := store.SaveSecrets(sec, pass); err != nil {
			return unlockedMsg{err: err}
		}
		return unlockedMsg{pass: pass, secrets: sec, report: rep}
	}
}

// persistSecrets writes the in-memory vault back. The status is what the caller
// wants shown once it lands, so a save that is part of a bigger action ("saved",
// "sync registered") reads as that action rather than as a vault write.
func (a *App) persistSecrets(status string) tea.Cmd {
	store, sec, pass := a.store, a.secrets, a.pass
	return func() tea.Msg {
		if err := store.SaveSecrets(sec, pass); err != nil {
			return secretsSavedMsg{err: err}
		}
		return secretsSavedMsg{status: status}
	}
}

// ── the gate ────────────────────────────────────────────────────────────────

// requireVault makes sure there is somewhere to put a secret before one is
// created, running after once there is. It reports whether the caller must stop
// and wait: a vault that is already open needs nothing.
//
// This is what keeps the "no secrets, no prompt" rule true. A user on keys and
// ssh-agent alone reaches this function exactly never.
func (a *App) requireVault(why string, after func(*App) tea.Cmd) (tea.Cmd, bool) {
	if a.pass != "" {
		return nil, false
	}
	u := newUnlock(unlockCreate, why)
	if a.store.HasVault() {
		u = newUnlock(unlockOpen, why)
	}
	u.after = after
	a.unlock = u
	return textinput.Blink, true
}

// handleUnlockKey owns the entire keyboard while the gate is up.
func (a *App) handleUnlockKey(msg tea.KeyMsg) tea.Cmd {
	u := a.unlock
	if u == nil {
		return nil
	}
	// A decrypt in flight takes no input at all: the answer is already on its
	// way and a second attempt would race it.
	if u.busy {
		return nil
	}

	switch a.keys.Action(ctxUnlock, msg.String()) {
	case actUnlockQuit:
		a.quitting = true
		return tea.Quit

	case actUnlockCancel:
		// Backing out is only possible when the gate is not the startup one:
		// there is nothing behind it at startup to back out to.
		if u.after == nil && u.mode == unlockOpen && !u.plaintext.Any() {
			return nil
		}
		if u.after != nil {
			a.unlock = nil
			a.status = "cancelled · nothing was saved"
			return nil
		}
		return nil

	case actUnlockField:
		if u.mode == unlockCreate {
			u.second = !u.second
			u.focusCurrent()
		}
		return nil

	case actUnlockSubmit:
		return a.submitUnlock()
	}

	var cmd tea.Cmd
	if u.mode == unlockCreate && u.second {
		u.confirm, cmd = u.confirm.Update(msg)
	} else {
		u.input, cmd = u.input.Update(msg)
	}
	return cmd
}

func (u *unlockState) focusCurrent() {
	u.input.Blur()
	u.confirm.Blur()
	if u.second {
		u.confirm.Focus()
	} else {
		u.input.Focus()
	}
}

func (a *App) submitUnlock() tea.Cmd {
	u := a.unlock
	pass := u.input.Value()

	if u.mode == unlockCreate {
		// The strength of this one string is the whole of the design's
		// security, so a short one is refused rather than warned about.
		if len(pass) < vault.MinPassphraseLen {
			u.err = fmt.Sprintf("use at least %d characters — this passphrase is the only thing protecting the vault", vault.MinPassphraseLen)
			return nil
		}
		if !u.second {
			u.second = true
			u.focusCurrent()
			return nil
		}
		if u.confirm.Value() != pass {
			u.err = "the two entries do not match"
			u.confirm.SetValue("")
			return nil
		}
		u.busy, u.err = true, ""
		return createVaultCmd(a.store, pass, u.plaintext)
	}

	if pass == "" {
		return nil
	}
	u.busy, u.err = true, ""
	return unlockCmd(a.store, pass, u.plaintext)
}

// applyUnlocked is the Update handler for the answer.
func (a *App) applyUnlocked(msg unlockedMsg) tea.Cmd {
	u := a.unlock
	if u == nil {
		return nil
	}
	u.busy = false

	if msg.err != nil {
		if errors.Is(msg.err, vault.ErrBadPassphrase) {
			u.attempt++
			u.input.SetValue("")
			if u.attempt >= maxUnlockAttempts {
				a.quitting = true
				return tea.Quit
			}
			u.err = fmt.Sprintf("wrong passphrase · %d of %d attempts left",
				maxUnlockAttempts-u.attempt, maxUnlockAttempts)
			return nil
		}
		u.err = firstLineOf(msg.err)
		return nil
	}

	a.pass = msg.pass
	a.secrets = msg.secrets
	after := u.after
	a.unlock = nil

	if msg.report.Passwords > 0 || msg.report.Keys > 0 {
		a.warning = fmt.Sprintf("migrated %s and %s into the vault · plaintext backup: %s",
			plural(msg.report.Passwords, "password"), plural(msg.report.Keys, "key"),
			msg.report.BackupPath)
	}

	if after != nil {
		return after(a)
	}
	return nil
}

// gateStartup decides whether the app opens on a lock screen.
//
// It only ever does when there is something encrypted to open, or something in
// the clear that must not stay that way. A user authenticating with keys and
// ssh-agent has no vault and is never asked for a passphrase — making them
// invent one to protect nothing is how a security feature becomes a ritual.
func (a *App) gateStartup(msg startupScanMsg) tea.Cmd {
	if a.pass != "" {
		return nil // --pull already unlocked it before the UI existed
	}
	switch {
	case msg.hasVault:
		u := newUnlock(unlockOpen, "")
		u.plaintext = msg.plaintext
		a.unlock = u
	case msg.plaintext.Any():
		u := newUnlock(unlockCreate,
			"Secrets were found stored in plaintext.\nChoose a passphrase to move them into an encrypted vault.")
		u.plaintext = msg.plaintext
		a.unlock = u
	default:
		return nil
	}
	return textinput.Blink
}

// hasSecretsFor reports whether the vault holds anything for a server, so a
// delete only re-seals it when there is something to take out.
func (a *App) hasSecretsFor(id string) bool {
	_, pw := a.secrets.Passwords[id]
	_, key := a.secrets.Keys[id]
	_, kp := a.secrets.KeyPass[id]
	return pw || key || kp
}

// ── key passphrase ──────────────────────────────────────────────────────────

// keyPassState asks for the passphrase of a locked private key. It is a
// different question from the vault's — this one unlocks a key, not the store —
// and the answer goes into the vault, so it is asked once per key rather than
// once per connection.
type keyPassState struct {
	input textinput.Model
	srv   model.Server
	sftp  bool // the failed attempt was the file connection, not a shell
	err   string
}

// askKeyPassphrase turns ErrKeyPassphraseRequired into the prompt. It reports
// whether it took the failure over from the error card.
func (a *App) askKeyPassphrase(srv model.Server, isSFTP bool) bool {
	ti := textinput.New()
	ti.Placeholder = "key passphrase"
	ti.Prompt = "› "
	ti.EchoMode = textinput.EchoPassword
	ti.EchoCharacter = '•'
	ti.CharLimit = 256
	ti.Width = 40
	ti.Focus()

	a.keyPass = &keyPassState{input: ti, srv: srv, sftp: isSFTP}
	return true
}

func (a *App) handleKeyPassKey(msg tea.KeyMsg) tea.Cmd {
	k := a.keyPass
	switch msg.String() {
	case "esc", "ctrl+c":
		a.keyPass = nil
		a.status = "cancelled"
		return nil
	case "enter":
		pass := k.input.Value()
		if pass == "" {
			return nil
		}
		srv, isSFTP := k.srv, k.sftp
		a.keyPass = nil

		// A key passphrase is a secret like any other, so it needs a vault. If
		// there is none yet this is the moment one is created.
		store := func(app *App) tea.Cmd {
			app.secrets.SetKeyPass(srv.ID, pass)
			retry := app.openTab(srv, true)
			if isSFTP {
				retry = app.startSFTP(srv)
			}
			return tea.Batch(app.persistSecrets(""), retry)
		}
		if cmd, waiting := a.requireVault(
			"A key passphrase is a secret, so it needs a vault.\nChoose a passphrase for it.", store); waiting {
			return cmd
		}
		return store(a)
	}

	var cmd tea.Cmd
	k.input, cmd = k.input.Update(msg)
	return cmd
}

// keyPassView is drawn where the confirmation panel would be: it is the same
// kind of thing, one question owning the keyboard.
func (a *App) keyPassView() string {
	k := a.keyPass
	var b strings.Builder
	b.WriteString(styleFormTitle.Render("Key passphrase"))
	b.WriteString("\n\n")
	b.WriteString(k.srv.Title() + " · " + k.srv.UserHost())
	b.WriteString("\n\n")
	b.WriteString("This key is passphrase-protected. The answer is kept in the\n")
	b.WriteString("vault, so you will not be asked for it again.")
	b.WriteString("\n\n")
	b.WriteString(k.input.View())
	b.WriteString("\n\n")
	if k.err != "" {
		b.WriteString(styleError.Render("✗ " + k.err))
		b.WriteString("\n")
	}
	b.WriteString(styleHint.Render("enter unlock and connect · esc cancel"))
	return b.String()
}

// pullStatus summarises what a pull did, host keys included: the union is the
// part a user has to be able to trust without checking.
func pullStatus(rep config.ApplyReport) string {
	s := fmt.Sprintf("pulled · %s", plural(rep.Servers, "server"))
	if rep.KnownHostsNew > 0 {
		s += fmt.Sprintf(" · %s added to known_hosts", plural(rep.KnownHostsNew, "host key"))
	}
	if rep.BackupPath != "" {
		s += " · backup: " + rep.BackupPath
	}
	return s
}

// ── view ────────────────────────────────────────────────────────────────────

// unlockView draws the whole screen. It replaces the frame rather than floating
// over it: there is nothing to float over yet, and a list of host names peeking
// out from behind a lock screen would defeat the point of encrypting them.
func (a *App) unlockView() string {
	u := a.unlock

	var b strings.Builder
	title := "Unlock"
	if u.mode == unlockCreate {
		title = "Create vault"
	}
	b.WriteString(styleFormTitle.Render(title))
	b.WriteString("\n\n")

	switch {
	case u.why != "":
		b.WriteString(u.why)
	case u.mode == unlockCreate:
		b.WriteString("Your passwords and private keys will be encrypted with this\n")
		b.WriteString("passphrase. It is never stored anywhere — if you lose it, the\n")
		b.WriteString("vault cannot be opened by anyone, including you.")
	default:
		b.WriteString("Enter the passphrase for " + a.store.VaultPath())
	}
	b.WriteString("\n\n")

	if u.plaintext.Any() {
		b.WriteString(styleWarning.Render(fmt.Sprintf(
			"⚠ found %s and %s stored in plaintext — they will be moved into the vault",
			plural(len(u.plaintext.Passwords), "password"),
			plural(len(u.plaintext.Keys), "key"))))
		b.WriteString("\n\n")
	}

	b.WriteString(u.input.View())
	b.WriteString("\n")
	if u.mode == unlockCreate {
		b.WriteString(u.confirm.View())
		b.WriteString("\n")
	}

	b.WriteString("\n")
	switch {
	case u.busy:
		b.WriteString(styleStatus.Render("unlocking…"))
	case u.err != "":
		b.WriteString(styleError.Render("✗ " + u.err))
	default:
		hint := "enter unlock · ctrl+c quit"
		if u.mode == unlockCreate {
			hint = "enter continue · tab switch field · ctrl+c quit"
		}
		b.WriteString(styleHint.Render(hint))
	}

	box := styleModal.Render(lipgloss.NewStyle().Padding(0, 2).Render(b.String()))
	return a.centreFrame(box)
}

// centreFrame puts one box in the middle of an otherwise empty screen of
// exactly the terminal's size — every row padded to width, height rows in all,
// which is the invariant the layout tests pin everywhere else.
func (a *App) centreFrame(box string) string {
	boxLines := strings.Split(box, "\n")
	top := maxInt((a.height-len(boxLines))/2, 0)

	out := make([]string, 0, a.height)
	for range top {
		out = append(out, padLine("", a.width))
	}
	for _, l := range boxLines {
		if len(out) >= a.height {
			break
		}
		left := maxInt((a.width-ansi.StringWidth(l))/2, 0)
		out = append(out, padLine(strings.Repeat(" ", left)+l, a.width))
	}
	for len(out) < a.height {
		out = append(out, padLine("", a.width))
	}
	return strings.Join(out[:a.height], "\n")
}
