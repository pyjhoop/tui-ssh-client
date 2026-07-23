package ui

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/model"
	"github.com/pyjhoop/tui-ssh-client/internal/vault"
)

// countingGitHub is a stand-in API that records every request, so a test can
// assert that none were made at all.
type countingGitHub struct {
	requests int
	private  bool
	sha      string
	body     []byte // ciphertext currently "in the repo"
	lastPut  []byte
}

func (g *countingGitHub) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.requests++
		switch {
		case !strings.Contains(r.URL.Path, "/contents/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"private": g.private})
		case r.Method == http.MethodGet:
			if g.body == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": g.sha, "encoding": "base64",
				"content": base64.StdEncoding.EncodeToString(g.body),
			})
		default:
			raw, _ := io.ReadAll(r.Body)
			var in map[string]string
			_ = json.Unmarshal(raw, &in)
			if in["sha"] != g.sha {
				w.WriteHeader(http.StatusConflict)
				return
			}
			g.lastPut, _ = base64.StdEncoding.DecodeString(in["content"])
			g.body, g.sha = g.lastPut, "sha-next"
			_ = json.NewEncoder(w).Encode(map[string]any{"content": map[string]string{"sha": g.sha}})
		}
	}))
	t.Cleanup(srv.Close)

	old := syncBase
	syncBase = srv.URL
	t.Cleanup(func() { syncBase = old })
}

func registered(sha string) *vault.GitHubAuth {
	return &vault.GitHubAuth{Owner: "acme", Repo: "dotfiles", Path: "ssh-client.age", Token: "ghp_x", SHA: sha}
}

// TestSyncDisabledMakesNoRequests is the opt-in rule as a test: before a
// repository is registered, nothing in the sync package runs. Not at startup,
// not on the keys that would use it.
func TestSyncDisabledMakesNoRequests(t *testing.T) {
	g := &countingGitHub{private: true}
	g.start(t)

	app, m := withVault(t, secretsWith("1", "pw"), "the passphrase")
	// Everything startup does, minus the pumps that only ever wait on channels.
	m = drain(t, m, loadServers(app.store))
	m = drain(t, m, loadUIState(app.store))
	m = drain(t, m, scanStartup(app.store))

	// Then the keys an idle list offers, the sync ones included.
	for _, key := range []string{"s", "i", "S", "P"} {
		if cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}); cmd != nil {
			m = drain(t, m, cmd)
		}
	}
	m.View()

	if g.requests != 0 {
		t.Fatalf("sync made %d requests before being set up", g.requests)
	}
	if !strings.Contains(app.status, "not set up") {
		t.Errorf("S should say sync is not set up, got %q", app.status)
	}
}

// TestSetupRefusesAPublicRepo: the check runs before the token is stored, so a
// failed registration leaves nothing behind at all.
func TestSetupRefusesAPublicRepo(t *testing.T) {
	g := &countingGitHub{private: false}
	g.start(t)

	app, m := withVault(t, secretsWith("1", "pw"), "the passphrase")

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})
	if app.syncForm == nil {
		t.Fatal("Y did not open the sync form")
	}
	typeInto(app, "acme/dotfiles")
	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	app.handleKey(tea.KeyMsg{Type: tea.KeyTab}) // straight to the token
	typeInto(app, "ghp_x")

	cmd := app.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no check")
	}
	m = drain(t, m, cmd)

	if app.secrets.GitHub != nil {
		t.Fatal("a public repository must not be registered")
	}
	if app.syncForm == nil || !strings.Contains(app.syncForm.err, "public") {
		t.Fatalf("the refusal should be on the form, got %+v", app.syncForm)
	}
	if g.lastPut != nil {
		t.Fatal("something was uploaded to a public repository")
	}
}

func TestSetupStoresTheTokenOnlyAfterTheCheck(t *testing.T) {
	g := &countingGitHub{private: true}
	g.start(t)

	app, m := withVault(t, secretsWith("1", "pw"), "the passphrase")

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})
	typeInto(app, "acme/dotfiles")
	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	app.handleKey(tea.KeyMsg{Type: tea.KeyTab})
	typeInto(app, "ghp_secret")
	m = drain(t, m, app.handleKey(tea.KeyMsg{Type: tea.KeyEnter}))

	if app.secrets.GitHub == nil || app.secrets.GitHub.Token != "ghp_secret" {
		t.Fatalf("the token was not stored: %+v", app.secrets.GitHub)
	}
	// And it is in the vault on disk, encrypted.
	raw, err := os.ReadFile(app.store.VaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ghp_secret") {
		t.Error("the token is readable in vault.age")
	}
	if app.rightMode == rightSync {
		t.Error("the form should have closed")
	}
}

// TestPushUploadsCiphertextOnly walks the whole push: the bundle is sealed
// before it leaves, so nothing recognisable reaches the wire.
func TestPushUploadsCiphertextOnly(t *testing.T) {
	g := &countingGitHub{private: true, sha: ""}
	g.start(t)

	sec := secretsWith("1", "hunter2")
	sec.GitHub = registered("")
	app, m := withVault(t, sec, "the passphrase")
	if err := app.store.Save([]model.Server{
		{ID: "1", Name: "prod-web", Host: "secret.example", Port: 22, User: "deploy", Auth: model.AuthPassword},
	}); err != nil {
		t.Fatal(err)
	}

	m = drain(t, m, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}}))

	if g.lastPut == nil {
		t.Fatal("nothing was uploaded")
	}
	for _, needle := range []string{"prod-web", "secret.example", "hunter2", "deploy", "ghp_x"} {
		if strings.Contains(string(g.lastPut), needle) {
			t.Errorf("the upload leaks %q", needle)
		}
	}
	if !strings.Contains(app.status, "synced") {
		t.Errorf("status: %q", app.status)
	}
	if app.secrets.GitHub.SHA != "sha-next" {
		t.Errorf("the new sha was not kept: %q", app.secrets.GitHub.SHA)
	}
}

// TestStalePushIsRefused: two machines, both pushing. The second one is told to
// pull rather than having its list merged for it.
func TestStalePushIsRefused(t *testing.T) {
	g := &countingGitHub{private: true, sha: "remote-moved-on", body: []byte("x")}
	g.start(t)

	sec := secretsWith("1", "pw")
	sec.GitHub = registered("what-we-last-saw")
	app, m := withVault(t, sec, "the passphrase")

	m = drain(t, m, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'S'}}))

	if !strings.Contains(app.errMsg, "pull first") {
		t.Fatalf("a stale push should say to pull first, got %q", app.errMsg)
	}
}

// TestPullAsksBeforeReplacing: a pull replaces the whole list, so it goes
// through the confirmation panel first.
func TestPullAsksBeforeReplacing(t *testing.T) {
	g := &countingGitHub{private: true}
	g.start(t)
	seedRemote(t, g, "the passphrase", []model.Server{
		{ID: "9", Name: "from-remote", Host: "r.example", Port: 22, User: "u", Auth: model.AuthAgent},
	})

	sec := secretsWith("1", "pw")
	sec.GitHub = registered(g.sha)
	app, m := withVault(t, sec, "the passphrase")
	if err := app.store.Save([]model.Server{
		{ID: "1", Name: "local-only", Host: "l.example", Port: 22, User: "u", Auth: model.AuthAgent},
	}); err != nil {
		t.Fatal(err)
	}
	m = drain(t, m, loadServers(app.store))

	m = drain(t, m, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}}))

	if app.confirm == nil {
		t.Fatal("a pull must ask before replacing the list")
	}
	if servers, _ := app.store.Load(); len(servers) != 1 || servers[0].Name != "local-only" {
		t.Fatalf("the list was replaced before the answer: %+v", servers)
	}

	// Saying no leaves everything alone.
	cmd, _ := app.confirm.resolve(app.keys, tea.KeyMsg{Type: tea.KeyEsc})
	app.confirm = nil
	if cmd != nil {
		m = drain(t, m, cmd)
	}
	if servers, _ := app.store.Load(); servers[0].Name != "local-only" {
		t.Fatalf("cancelling still replaced the list: %+v", servers)
	}

	// Saying yes applies it.
	m = drain(t, m, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}}))
	if app.confirm == nil {
		t.Fatal("no confirmation the second time")
	}
	cmd, _ = app.confirm.resolve(app.keys, tea.KeyMsg{Type: tea.KeyEnter})
	app.confirm = nil
	m = drain(t, m, cmd)

	servers, err := app.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "from-remote" {
		t.Fatalf("the pull did not land: %+v", servers)
	}
	if !strings.Contains(app.status, "pulled") {
		t.Errorf("status: %q", app.status)
	}
	// The registration survives the replacement: the token that just worked is
	// this machine's, not the bundle's.
	if app.secrets.GitHub == nil || app.secrets.GitHub.Token != "ghp_x" {
		t.Fatalf("the sync registration was lost: %+v", app.secrets.GitHub)
	}
}

// TestPullKeepsOpenTabs: a session is a connection that is already made. A list
// that changed under it says nothing about that.
func TestPullKeepsOpenTabs(t *testing.T) {
	g := &countingGitHub{private: true}
	g.start(t)
	seedRemote(t, g, "the passphrase", []model.Server{
		{ID: "9", Name: "from-remote", Host: "r.example", Port: 22, User: "u", Auth: model.AuthAgent},
	})

	sec := secretsWith("1", "pw")
	sec.GitHub = registered(g.sha)
	app, m := withVault(t, sec, "the passphrase")

	tab := attachTab(app, "local-only")
	before := tab.emu()

	m = drain(t, m, app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}}))
	if app.confirm == nil {
		t.Fatalf("a pull must ask first · status=%q err=%q", app.status, app.errMsg)
	}
	cmd, _ := app.confirm.resolve(app.keys, tea.KeyMsg{Type: tea.KeyEnter})
	app.confirm = nil
	m = drain(t, m, cmd)

	if len(app.tabs) != 1 || app.tabs[0] != tab {
		t.Fatalf("the pull disturbed the open tabs: %+v", app.tabs)
	}
	if tab.emu() != before {
		t.Error("the tab's emulator was replaced")
	}
}

// TestSyncFormSwallowsTabKeys: the registration form is a dialog like any
// other, so alt+2 must not switch the panel out from under it.
func TestSyncFormSwallowsTabKeys(t *testing.T) {
	app, _ := withVault(t, secretsWith("1", "pw"), "the passphrase")
	attachTab(app, "one")
	attachTab(app, "two")

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}})
	if app.syncForm == nil {
		t.Fatal("Y did not open the sync form")
	}
	active := app.active

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if app.active != active || app.rightMode != rightSync {
		t.Fatalf("a tab key got through the sync form: active=%d mode=%v", app.active, app.rightMode)
	}

	app.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if app.syncForm != nil || app.rightMode == rightSync {
		t.Error("esc should close the form")
	}
}

// seedRemote fills the fake repository with a properly sealed bundle.
func seedRemote(t *testing.T, g *countingGitHub, pass string, servers []model.Server) {
	t.Helper()
	src := config.New(t.TempDir())
	if err := src.Save(servers); err != nil {
		t.Fatal(err)
	}
	blob, err := src.Bundle(vault.Secrets{Version: vault.CurrentVersion})
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Encrypt(blob, pass)
	if err != nil {
		t.Fatal(err)
	}
	g.body, g.sha = cipher, "sha-remote"
}
