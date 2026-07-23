package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/pyjhoop/ssh-client/internal/config"
	syncpkg "github.com/pyjhoop/ssh-client/internal/sync"
	"github.com/pyjhoop/ssh-client/internal/vault"
)

// DefaultBundlePath is where the encrypted bundle lands in the repository. The
// name says what it is; the contents say nothing at all without the passphrase.
const DefaultBundlePath = "ssh-client.age"

// Sync is opt-in: until a repository is registered here, not one line of the
// sync package runs, no token exists, and the app makes no network request of
// its own at startup or at any other time.
//
// syncForm is that registration.
type syncForm struct {
	inputs  [syncFieldCount]textinput.Model
	focused int
	err     string
	busy    bool
	// current is the registration already on file, shown above the fields so
	// re-registering is visibly a replacement.
	current *vault.GitHubAuth
}

const (
	syncFieldRepo = iota
	syncFieldPath
	syncFieldToken
	syncFieldCount
)

var syncFieldLabels = [syncFieldCount]string{
	syncFieldRepo:  "Repository",
	syncFieldPath:  "Path in repo",
	syncFieldToken: "Token",
}

func newSyncForm(current *vault.GitHubAuth) *syncForm {
	f := &syncForm{current: current}
	mk := func(placeholder string) textinput.Model {
		ti := textinput.New()
		ti.Placeholder = placeholder
		ti.Prompt = "› "
		ti.CharLimit = 256
		ti.Width = 48
		return ti
	}
	f.inputs[syncFieldRepo] = mk("owner/repo · must be private")
	f.inputs[syncFieldPath] = mk(DefaultBundlePath)
	f.inputs[syncFieldToken] = mk("fine-grained PAT · Contents: Read and write")
	f.inputs[syncFieldToken].EchoMode = textinput.EchoPassword
	f.inputs[syncFieldToken].EchoCharacter = '•'

	if current != nil {
		f.inputs[syncFieldRepo].SetValue(current.Owner + "/" + current.Repo)
		f.inputs[syncFieldPath].SetValue(current.Path)
	}
	f.focus(syncFieldRepo)
	return f
}

func (f *syncForm) focus(i int) {
	for j := range f.inputs {
		f.inputs[j].Blur()
	}
	f.focused = i
	f.inputs[i].Focus()
}

func (f *syncForm) move(delta int) {
	f.focus((f.focused + delta + syncFieldCount) % syncFieldCount)
}

// auth builds the registration from the fields. The token may be left blank
// when re-registering: keeping the one already in the vault is the common case.
func (f *syncForm) auth(existing *vault.GitHubAuth) (vault.GitHubAuth, error) {
	owner, name, err := syncpkg.ParseRepo(f.inputs[syncFieldRepo].Value())
	if err != nil {
		return vault.GitHubAuth{}, err
	}
	path := strings.TrimSpace(f.inputs[syncFieldPath].Value())
	if path == "" {
		path = DefaultBundlePath
	}
	token := strings.TrimSpace(f.inputs[syncFieldToken].Value())
	if token == "" && existing != nil {
		token = existing.Token
	}
	if token == "" {
		return vault.GitHubAuth{}, errors.New("a token is required")
	}
	return vault.GitHubAuth{Owner: owner, Repo: name, Path: path, Token: token}, nil
}

func (f *syncForm) View() string {
	var b strings.Builder

	if f.current != nil {
		b.WriteString(styleStatus.Render("registered: " + f.current.Owner + "/" + f.current.Repo + " · " + f.current.Path))
		b.WriteString("\n\n")
	}

	for i := range syncFieldCount {
		style := styleFormLabel
		if i == f.focused {
			style = styleFormLabelFocused
		}
		b.WriteString(style.Render(syncFieldLabels[i]))
		b.WriteString("\n")
		b.WriteString(f.inputs[i].View())
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(styleHint.Render(
		"A fine-grained token scoped to this one repository with\n" +
			"Contents: Read and write is all this needs. The repository is\n" +
			"checked for being private before anything is ever uploaded, and\n" +
			"again before every push."))
	b.WriteString("\n\n")

	switch {
	case f.busy:
		b.WriteString(styleStatus.Render("checking the repository…"))
	case f.err != "":
		b.WriteString(styleError.Render("✗ " + f.err))
	default:
		b.WriteString(styleHint.Render("tab move · enter save · esc cancel"))
	}
	return b.String()
}

// ── messages ────────────────────────────────────────────────────────────────

// syncCheckedMsg is the answer to the private-repository check done at
// registration. Nothing is stored until it passes.
type syncCheckedMsg struct {
	auth vault.GitHubAuth
	err  error
}

type syncPushedMsg struct {
	sha     string
	servers int
	err     error
}

// syncFetchedMsg is a pull that has arrived and decrypted but has not been
// applied: the user still has to say yes to replacing their list.
type syncFetchedMsg struct {
	blob    []byte
	sha     string
	servers int
	updated time.Time
	device  string
	err     error
}

type syncAppliedMsg struct {
	report  config.ApplyReport
	secrets vault.Secrets
	sha     string
	err     error
}

// ── commands ────────────────────────────────────────────────────────────────

// syncBase overrides the GitHub API root. It is empty in every build; tests
// point it at an httptest server, which is the only way to exercise these paths
// without a network.
var syncBase string

func remoteFor(auth vault.GitHubAuth) (*syncpkg.Remote, syncpkg.Repo) {
	return &syncpkg.Remote{Token: auth.Token, Base: syncBase},
		syncpkg.Repo{Owner: auth.Owner, Name: auth.Repo, Path: auth.Path, Branch: auth.Branch}
}

func checkRepoCmd(auth vault.GitHubAuth) tea.Cmd {
	return func() tea.Msg {
		remote, repo := remoteFor(auth)
		if err := remote.Check(repo); err != nil {
			return syncCheckedMsg{err: err}
		}
		return syncCheckedMsg{auth: auth}
	}
}

// pushCmd re-checks that the repository is still private, then uploads the
// encrypted bundle under the sha we last saw.
//
// The check is repeated on every push rather than trusted from registration: a
// repository can be flipped to public at any time, and the moment it is, this
// upload would publish every host name we have.
func pushCmd(store *config.Store, sec vault.Secrets, pass string) tea.Cmd {
	return func() tea.Msg {
		if sec.GitHub == nil {
			return syncPushedMsg{err: errors.New("sync is not set up")}
		}
		remote, repo := remoteFor(*sec.GitHub)
		if err := remote.Check(repo); err != nil {
			return syncPushedMsg{err: err}
		}

		blob, err := store.Bundle(sec)
		if err != nil {
			return syncPushedMsg{err: err}
		}
		cipher, err := vault.Encrypt(blob, pass)
		if err != nil {
			return syncPushedMsg{err: err}
		}
		n, _, _, _ := config.BundleServers(blob)

		msg := fmt.Sprintf("ssh-client: %d servers", n)
		sha, err := remote.Put(repo, cipher, sec.GitHub.SHA, msg)
		if err != nil {
			return syncPushedMsg{err: err}
		}
		return syncPushedMsg{sha: sha, servers: n}
	}
}

// pullCmd fetches and decrypts, and stops there. Replacing the local list is a
// separate step behind a confirmation: a pull the user did not mean would
// otherwise silently discard servers that only exist on this machine.
func pullCmd(sec vault.Secrets, pass string) tea.Cmd {
	return func() tea.Msg {
		if sec.GitHub == nil {
			return syncFetchedMsg{err: errors.New("sync is not set up")}
		}
		remote, repo := remoteFor(*sec.GitHub)
		cipher, sha, err := remote.Get(repo)
		if err != nil {
			return syncFetchedMsg{err: err}
		}
		blob, err := vault.Decrypt(cipher, pass)
		if err != nil {
			return syncFetchedMsg{err: err}
		}
		n, updated, device, err := config.BundleServers(blob)
		if err != nil {
			return syncFetchedMsg{err: err}
		}
		return syncFetchedMsg{blob: blob, sha: sha, servers: n, updated: updated, device: device}
	}
}

// applyPullCmd is the confirmed half: replace the list and the secrets, union
// the host keys, and re-seal the vault under the same passphrase.
func applyPullCmd(store *config.Store, blob []byte, sha string, sec vault.Secrets, pass string) tea.Cmd {
	return func() tea.Msg {
		// The registration is this machine's, not the bundle's: the token we are
		// holding is the one that just worked. Carry it across the replacement.
		auth := sec.GitHub
		rep, err := store.ApplyBundle(blob, &sec)
		if err != nil {
			return syncAppliedMsg{err: err}
		}
		if auth != nil {
			copy := *auth
			copy.SHA = sha
			sec.GitHub = &copy
		}
		if err := store.SaveSecrets(sec, pass); err != nil {
			return syncAppliedMsg{err: err}
		}
		return syncAppliedMsg{report: rep, secrets: sec, sha: sha}
	}
}

// ── keys and flow ───────────────────────────────────────────────────────────

// openSync shows the registration form. It needs a vault first: the token is a
// secret, and there is nowhere to put it until there is one.
func (a *App) openSync() tea.Cmd {
	if cmd, waiting := a.requireVault(
		"Sync stores a GitHub token, so it needs a vault first.\nChoose a passphrase for it.",
		(*App).openSync); waiting {
		return cmd
	}
	a.syncForm = newSyncForm(a.secrets.GitHub)
	a.prevRight = a.rightMode
	a.rightMode = rightSync
	a.focus = focusSync
	a.errMsg, a.status = "", ""
	return textinput.Blink
}

func (a *App) closeSync() {
	a.syncForm = nil
	a.rightMode = a.prevRight
	a.focus = focusSidebar
}

// handleSyncKey owns the keyboard while the registration form is up.
func (a *App) handleSyncKey(msg tea.KeyMsg) tea.Cmd {
	f := a.syncForm
	if f == nil {
		a.focus = focusSidebar
		return nil
	}
	if f.busy {
		return nil
	}

	switch msg.String() {
	case "esc":
		a.closeSync()
		return nil
	case "tab", "down":
		f.move(1)
		return nil
	case "shift+tab", "up":
		f.move(-1)
		return nil
	case "enter":
		auth, err := f.auth(a.secrets.GitHub)
		if err != nil {
			f.err = err.Error()
			return nil
		}
		f.busy, f.err = true, ""
		return checkRepoCmd(auth)
	}

	var cmd tea.Cmd
	f.inputs[f.focused], cmd = f.inputs[f.focused].Update(msg)
	return cmd
}

// startPush uploads. It refuses before there is a registration rather than
// prompting for one: pressing S by accident must not start a setup flow.
func (a *App) startPush() tea.Cmd {
	if a.secrets.GitHub == nil {
		a.status = "sync is not set up · press Y to register a private repo"
		return nil
	}
	a.status = "pushing…"
	a.errMsg = ""
	return pushCmd(a.store, a.secrets, a.pass)
}

func (a *App) startPull() tea.Cmd {
	if a.secrets.GitHub == nil {
		a.status = "sync is not set up · press Y to register a private repo"
		return nil
	}
	a.status = "pulling…"
	a.errMsg = ""
	return pullCmd(a.secrets, a.pass)
}

// pullConfirm is the preview. A pull replaces the whole list, so the number of
// servers and where the backup will be are on screen before enter means yes.
func (a *App) pullConfirm(msg syncFetchedMsg) *confirm {
	lines := []string{
		fmt.Sprintf("Replace the local list with the remote version?"),
		"",
		fmt.Sprintf("remote: %s · %s", plural(msg.servers, "server"), msg.updated.Local().Format("2006-01-02 15:04")),
		fmt.Sprintf("local:  %s", plural(len(a.servers), "server")),
	}
	if msg.device != "" {
		lines = append(lines, "pushed from "+msg.device)
	}
	lines = append(lines, "", "The current list is backed up to "+localBackupHint)

	return &confirm{
		title:  "Pull",
		lines:  lines,
		warn:   "⚠ servers that exist only on this machine will be gone",
		accept: "[enter] replace",
		onYes:  applyPullCmd(a.store, msg.blob, msg.sha, a.secrets, a.pass),
	}
}

// localBackupHint mirrors config's backup file name. It is duplicated rather
// than exported because it is a sentence fragment here, not a path the ui may
// build one from.
const localBackupHint = "ssh-client.local.bak in the config directory"

// syncAdvice maps the sync sentinels to something the user can act on. It is
// errorcard's rule applied to a second package: sentinels only, never text.
func syncAdvice(err error) string {
	switch {
	case errors.Is(err, syncpkg.ErrRepoPublic):
		return "that repository is public — refusing to upload. Make it private and try again."
	case errors.Is(err, syncpkg.ErrBadToken):
		return "the token was rejected. It needs Contents: Read and write on that repository."
	case errors.Is(err, syncpkg.ErrRepoNotFound):
		return "no such repository or file. Push once (S) before pulling on a new machine."
	case errors.Is(err, syncpkg.ErrSyncConflict):
		return "remote is newer — pull first (P)"
	case errors.Is(err, vault.ErrBadPassphrase):
		return "the remote bundle was sealed with a different passphrase."
	default:
		return firstLineOf(err)
	}
}
