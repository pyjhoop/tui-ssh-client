package ui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/model"
	"github.com/pyjhoop/tui-ssh-client/internal/vault"
)

// ── helpers ─────────────────────────────────────────────────────────────────

// drain runs a command and everything it leads to, feeding each message back
// into the model. It stands in for the Bubble Tea runtime, which the ui tests
// do without.
func drain(t *testing.T, m tea.Model, cmd tea.Cmd) tea.Model {
	t.Helper()
	for depth := 0; cmd != nil && depth < 32; depth++ {
		msg, ok := run(cmd)
		if !ok || msg == nil {
			// A command that does not answer promptly is a pump waiting on a
			// channel (host keys, session output) or a cursor blink. Neither is
			// part of what these tests drive, so stopping there is the answer.
			return m
		}
		batch, isBatch := msg.(tea.BatchMsg)
		if !isBatch {
			m, cmd = m.Update(msg)
			continue
		}
		var next tea.Cmd
		for _, c := range batch {
			if c == nil {
				continue
			}
			sub, ok := run(c)
			if !ok || sub == nil {
				continue
			}
			var out tea.Cmd
			m, out = m.Update(sub)
			if out != nil && next == nil {
				next = out
			}
		}
		cmd = next
	}
	return m
}

// run executes one command, giving up on the ones that block forever. The
// budget is generous rather than tight: the crypto is slow on purpose, and
// under -race a scrypt round takes seconds. It is here to catch a pump waiting
// on a channel, not to measure anything.
const cmdBudget = 60 * time.Second

func run(cmd tea.Cmd) (tea.Msg, bool) {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg, true
	case <-time.After(cmdBudget):
		return nil, false
	}
}

func typeInto(app *App, s string) {
	for _, r := range s {
		app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// unlockWith answers whichever passphrase question is on screen and settles
// everything it sets off.
func unlockWith(t *testing.T, m tea.Model, app *App, pass string) tea.Model {
	t.Helper()
	if app.unlock == nil {
		t.Fatal("no unlock screen is up")
	}
	typeInto(app, pass)
	if app.unlock.mode == unlockCreate {
		app.handleKey(tea.KeyMsg{Type: tea.KeyEnter}) // move to the confirmation
		typeInto(app, pass)
	}
	cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("the unlock screen produced no command")
	}
	return drain(t, m, cmd)
}

// withVault builds an app whose config dir already holds a sealed vault, and
// opens it, so a test can start from "unlocked".
func withVault(t *testing.T, sec vault.Secrets, pass string) (*App, tea.Model) {
	t.Helper()
	store := config.New(t.TempDir())
	if err := store.SaveSecrets(sec, pass); err != nil {
		t.Fatal(err)
	}
	app := New(store)
	app.Unlocked(pass, sec)
	var m tea.Model = app
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return app, m
}

// ── the gate ────────────────────────────────────────────────────────────────

// TestNoVaultNoPrompt is the rule that keeps this feature from becoming a
// ritual: somebody who authenticates with keys and ssh-agent has no secrets, so
// there is nothing to unlock and they are never asked.
func TestNoVaultNoPrompt(t *testing.T) {
	store := config.New(t.TempDir())
	if err := store.Save([]model.Server{
		{ID: "1", Name: "web", Host: "a", Port: 22, User: "u", Auth: model.AuthAgent},
	}); err != nil {
		t.Fatal(err)
	}

	app := New(store)
	var m tea.Model = app
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = drain(t, m, scanStartup(store))

	if app.unlock != nil {
		t.Fatal("a config with no secrets must not ask for a passphrase")
	}
	if strings.Contains(stripANSI(m.View()), "passphrase") {
		t.Error("the lock screen was drawn anyway")
	}
}

// TestPlaintextConfigTriggersMigration: a pre-v6 config is the one case where
// the passphrase question comes up unasked, and it comes up as "create", not
// "unlock".
func TestPlaintextConfigTriggersMigration(t *testing.T) {
	dir := t.TempDir()
	store := config.New(dir)
	if err := writeLegacy(dir, `[{"id":"a","host":"a.example","port":22,"user":"root","auth":"password","password":"hunter2"}]`); err != nil {
		t.Fatal(err)
	}

	app := New(store)
	var m tea.Model = app
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drain(t, m, scanStartup(store))

	if app.unlock == nil || app.unlock.mode != unlockCreate {
		t.Fatalf("want a create-vault screen, got %+v", app.unlock)
	}
	if !app.unlock.plaintext.Any() {
		t.Fatal("the plaintext that was found is not on the screen's state")
	}

	m = unlockWith(t, m, app, "a good passphrase")

	if app.unlock != nil {
		t.Fatal("still locked after a successful create")
	}
	if app.secrets.Password("a") != "hunter2" {
		t.Fatalf("the password was not migrated: %+v", app.secrets)
	}
	if !strings.Contains(app.warning, "migrated") {
		t.Errorf("the migration should be announced, got %q", app.warning)
	}
}

// TestUnlockSwallowsEverything: while the gate is up, no key and no click may
// reach anything behind it — there is nothing behind it that should be
// reachable, including the server names the vault exists to hide.
func TestUnlockSwallowsEverything(t *testing.T) {
	app, m := withVault(t, secretsWith("1", "pw"), "the passphrase")
	// Re-lock: this is the startup path, not the pre-unlocked one.
	app.pass, app.secrets = "", vault.Secrets{}
	app.servers = []model.Server{{ID: "1", Name: "prod-web", Host: "a", Port: 22, User: "u"}}
	app.sidebar.SetServers(app.servers)
	m = drain(t, m, scanStartup(app.store))
	if app.unlock == nil {
		t.Fatal("a vault on disk should put the gate up")
	}

	view := stripANSI(m.View())
	if strings.Contains(view, "prod-web") {
		t.Error("a server name is visible behind the lock screen")
	}

	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyRunes, Runes: []rune{'f'}},
		{Type: tea.KeyRunes, Runes: []rune{'d'}},
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune("2"), Alt: true},
		{Type: tea.KeyCtrlB},
	} {
		app.handleKey(key)
		if app.quitting {
			t.Fatalf("%v quit the app from the lock screen", key)
		}
		if app.rightMode != rightEmpty || len(app.tabs) != 0 {
			t.Fatalf("%v reached the app behind the lock screen", key)
		}
	}

	// The mouse is blocked the same way.
	before := app.focus
	m, _ = m.Update(tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if app.focus != before {
		t.Error("a click got through the lock screen")
	}
	if app.unlock == nil {
		t.Fatal("the gate went away without a passphrase")
	}
}

func TestWrongPassphraseThriceQuits(t *testing.T) {
	app, m := withVault(t, secretsWith("1", "pw"), "the passphrase")
	app.pass, app.secrets = "", vault.Secrets{}
	m = drain(t, m, scanStartup(app.store))

	for i := 1; i <= maxUnlockAttempts; i++ {
		if app.unlock == nil {
			t.Fatalf("attempt %d: the gate is gone", i)
		}
		typeInto(app, "not the passphrase")
		cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatalf("attempt %d: no command", i)
		}
		m, _ = m.Update(cmd())
	}

	if !app.quitting {
		t.Fatalf("after %d wrong passphrases the app should quit", maxUnlockAttempts)
	}
}

func TestRightPassphraseOpensTheVault(t *testing.T) {
	app, m := withVault(t, secretsWith("srv-1", "hunter2"), "the passphrase")
	app.pass, app.secrets = "", vault.Secrets{}
	m = drain(t, m, scanStartup(app.store))

	m = unlockWith(t, m, app, "the passphrase")

	if app.unlock != nil {
		t.Fatal("still locked")
	}
	if app.secrets.Password("srv-1") != "hunter2" {
		t.Fatalf("the vault contents did not arrive: %+v", app.secrets)
	}
	// And the ordinary frame is back: two panels, exact width.
	assertFrame(t, app, m)
}

// TestLayoutAlignmentWithUnlockAndSync: the two new screens obey the same
// geometry as everything else — every row exactly width, exactly height rows.
func TestLayoutAlignmentWithUnlockAndSync(t *testing.T) {
	for _, size := range [][2]int{{100, 24}, {80, 30}, {200, 60}, {60, 14}} {
		width, height := size[0], size[1]

		app, m := withVault(t, secretsWith("1", "pw"), "the passphrase")
		m, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})

		// The lock screen.
		app.pass, app.secrets = "", vault.Secrets{}
		m = drain(t, m, scanStartup(app.store))
		checkFrameSize(t, "unlock", m.View(), width, height)

		// With an error on it, which is a longer box.
		app.unlock.err = "wrong passphrase · 2 of 3 attempts left"
		checkFrameSize(t, "unlock+error", m.View(), width, height)

		// The sync form.
		app.unlock = nil
		app.pass = "the passphrase"
		app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})
		if app.syncForm == nil {
			t.Fatal("Y did not open the sync form")
		}
		checkFrameSize(t, "sync", m.View(), width, height)

		app.syncForm.err = "that repository is public — refusing to upload."
		checkFrameSize(t, "sync+error", m.View(), width, height)
	}
}

func checkFrameSize(t *testing.T, what, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Errorf("%s at %dx%d: %d rows, want %d", what, width, height, len(lines), height)
		return
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != width {
			t.Errorf("%s at %dx%d: row %d is %d columns, want %d", what, width, height, i, w, width)
		}
	}
}

func assertFrame(t *testing.T, app *App, m tea.Model) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != app.height {
		t.Fatalf("frame is %d rows, want %d", len(lines), app.height)
	}
	if strings.Count(stripANSI(lines[topMargin]), "╭") != 2 {
		t.Errorf("the two panels are not back: %q", stripANSI(lines[topMargin]))
	}
}

func secretsWith(id, pw string) vault.Secrets {
	var s vault.Secrets
	s.Version = vault.CurrentVersion
	s.SetPassword(id, pw)
	return s
}

// writeLegacy plants a pre-v6 servers.json. It has to be written by hand:
// model.Server no longer has a password field to serialise, which is the point.
func writeLegacy(dir, body string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(config.New(dir).Path(), []byte(body), 0o600)
}
