// Package ui is the Bubble Tea layer: layout, focus, and the state machine that
// ties the config store and ssh sessions together. It never touches the
// filesystem or the network directly — those calls go through config and ssh,
// always inside a tea.Cmd.
package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
	"github.com/pyjhoop/tui-ssh-client/internal/model"
	sftppkg "github.com/pyjhoop/tui-ssh-client/internal/sftp"
	sshpkg "github.com/pyjhoop/tui-ssh-client/internal/ssh"
	"github.com/pyjhoop/tui-ssh-client/internal/vault"
)

type focusArea int

const (
	focusSidebar focusArea = iota
	focusForm
	focusSession
	focusLocal
	focusRemote
	focusImport
	focusSync
)

type rightMode int

const (
	rightEmpty rightMode = iota
	rightForm
	rightTerminal
	rightError
	rightSFTP
	rightImport
	rightSync
)

// escapeHint is the key that moves focus out of a live session.
const escapeHint = "ctrl+b"

// App is the root model.
type App struct {
	store *config.Store

	servers []model.Server
	sidebar sidebar
	form    form

	focus     focusArea
	rightMode rightMode

	// keys is the one place a key is written down. The handlers switch on the
	// actions it resolves, the status line and the help card render it, and
	// keys.json overlays it — so a shortcut that exists is documented and a
	// documented one exists.
	//
	// help is the floating card. It is a modal like any other, and the lightest
	// of them: it changes nothing, so closing it restores nothing.
	keys        *Keymap
	help        *helpState
	keyProblems []KeymapProblem

	width, height int

	// Session state. Each tab owns its session, its emulator and its scroll
	// offset; active is the one on screen, or -1 for none. gen is still one
	// app-wide counter, bumped on every dial, and every tab remembers the value
	// it was given — messages route by it, so output from a session that has
	// been superseded finds no tab and is dropped.
	//
	// Emulators come from pool and go back to it when a tab closes: they must
	// never be closed (see keyPump), so they are recycled instead.
	tabs   []*sessionTab
	active int
	pool   termPool
	gen    int

	// hostKeys carries trust-on-first-use questions from the dialing goroutine.
	// It is drained by the same pump pattern as session output.
	hostKeys chan *sshpkg.HostKeyPrompt

	// confirm, when set, replaces the right panel body and swallows every key
	// that is not an answer to it.
	confirm *confirm

	// Vault state. secrets is the decrypted contents and pass is the passphrase
	// that opened it — both live for the process only. The passphrase is never
	// written anywhere: a "remember on this device" option would turn the vault
	// into a padlock drawn on a plaintext file.
	//
	// unlock is the startup gate. While it is non-nil nothing else is drawn and
	// nothing else sees a key: it is the modal rule at its strictest.
	secrets vault.Secrets
	pass    string
	unlock  *unlockState

	// syncForm is the opt-in GitHub registration, non-nil only in rightSync.
	// Before it has ever been filled in there is no token, no repository and no
	// request — the sync package is not reached at all.
	syncForm *syncForm

	// keyPass asks for a passphrase that unlocks a private key. It is a separate
	// question from the vault's, and the answer goes into the vault so it is
	// asked once per key.
	keyPass *keyPassState

	// importing is the ~/.ssh/config preview, non-nil only in rightImport mode.
	// prevRight is the mode to fall back to when it is cancelled, so leaving the
	// preview never disturbs the open session tabs.
	importing *importer
	prevRight rightMode

	// SFTP state. The connection is deliberately separate from the terminal
	// session's: closing one leaves the other alive. sftpGen plays the same role
	// as gen — messages from a superseded connection are dropped.
	remote         *sftppkg.Remote
	local          filePane
	remotePane     filePane
	sftpGen        int
	sftpID         string // the server the panes are attached to
	sftpName       string
	sftpAddr       string
	connectingSFTP string

	// drag is non-nil only while files are being dragged between panes; pending
	// is the planned transfer waiting for confirmation, whether it came from a
	// drag or from the keyboard. scanning covers the walk in between, and
	// transfer is the copy itself — non-nil means one is running, which is what
	// blocks a second.
	// sftpErr is a failed connection, drawn as a card floating over the panes.
	drag     *dragState
	pending  *transferReq
	scanning bool
	transfer *transferState
	rename   *renameState
	sftpErr  error

	// lastAttempt is the server the current connect is for; the error card needs
	// it to offer retry and edit. lastWasSFTP says which of the two connections
	// failed, so [r] retries the one the user actually asked for.
	lastAttempt model.Server
	lastWasSFTP bool
	failErr     error

	// clip is an OSC 52 clipboard sequence waiting to go out. View writes it as a
	// prefix to exactly one frame and clipFlushMsg clears it: the renderer owns
	// stdout, so the only way to send a sequence is to put it in the frame.
	clip string

	status   string
	errMsg   string
	warning  string
	quitting bool
}

// New builds the root model.
func New(store *config.Store) *App {
	return &App{
		store:     store,
		focus:     focusSidebar,
		rightMode: rightEmpty,
		active:    -1,
		sidebar:   newSidebar(nil, sidebarWidth-2, 10),
		form:      newForm(40, 20),
		hostKeys:  make(chan *sshpkg.HostKeyPrompt, 1),
		keys:      DefaultKeymap(),
	}
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(
		loadServers(a.store),
		loadUIState(a.store),
		loadKeymap(a.store),
		scanStartup(a.store),
		waitForHostKey(a.hostKeys),
	)
}

// Unlocked pre-opens the vault, for the --pull bootstrap that has already had
// to ask for the passphrase before there was a UI to ask from.
func (a *App) Unlocked(pass string, sec vault.Secrets) {
	a.pass, a.secrets = pass, sec
}

// ── messages ────────────────────────────────────────────────────────────────

type serversLoadedMsg struct {
	servers []model.Server
	// removed is the entry a delete took out, so its secrets can be dropped
	// from the vault too. Leaving them behind would keep a password for a
	// server that no longer exists.
	removed string
	err     error
}

// serverSavedMsg reports a written server list. The secrets ride along rather
// than being written by the same command: the vault lives in App, and a command
// running off the UI goroutine may not touch it.
type serverSavedMsg struct {
	servers  []model.Server
	saved    model.Server
	password string
	keyBody  string
	editing  bool
	err      error
}

// uiStateLoadedMsg carries the fold and sort preferences. It has no error field
// on purpose: a missing or unreadable ui.json is a normal startup.
type uiStateLoadedMsg struct{ state config.UIState }

// keymapLoadedMsg carries keys.json. Unlike ui.json it does have an error
// field: a keymap the user wrote and we could not read is worth saying out loud.
type keymapLoadedMsg struct {
	keys map[string][]string
	err  error
}

// sshConfigParsedMsg is the result of reading ~/.ssh/config for the import
// preview. Parsing is file IO, so it never happens inside Update.
type sshConfigParsedMsg struct {
	path    string
	entries []config.SSHConfigEntry
	err     error
}

// serversImportedMsg reports a finished import: one Save for the whole batch.
type serversImportedMsg struct {
	servers  []model.Server
	imported int
	skipped  int
	err      error
}

type connectedMsg struct {
	gen     int
	session *sshpkg.Session
}

type connectFailedMsg struct {
	gen int
	err error
}

type outputMsg struct {
	gen  int
	data []byte
}

type sessionEndedMsg struct {
	gen int
	err error
}

// clipFlushMsg drops the clipboard sequence again, one frame after View sent it.
// The round trip is the point: it is what keeps the escape out of every frame
// but the one that carries it.
type clipFlushMsg struct{}

// reconnectMsg fires when a lost tab's backoff expires. It carries the
// generation so an attempt the user has already forced with [r] wins.
type reconnectMsg struct{ gen int }

// hostKeyPromptMsg carries a fingerprint question from a dialing goroutine that
// is blocked waiting for the answer.
type hostKeyPromptMsg struct {
	prompt *sshpkg.HostKeyPrompt
}

// hostKeyAnsweredMsg closes the confirm panel once the reply is on its way.
type hostKeyAnsweredMsg struct{}

// sftpConnectedMsg carries a live SFTP connection plus its first listing: the
// home directory has to be asked for over the wire, so it cannot be resolved
// later on the UI goroutine.
type sftpConnectedMsg struct {
	gen     int
	remote  *sftppkg.Remote
	dir     string
	entries []model.FileEntry
}

type sftpFailedMsg struct {
	gen int
	err error
}

// listedMsg is a directory listing for one of the two panes.
type listedMsg struct {
	gen     int
	side    focusArea
	dir     string
	entries []model.FileEntry
	err     error
}

// transferState is a copy in flight. The goroutine owns prog and nothing else;
// the UI reads it on a tick rather than being messaged per chunk, which would
// be thousands of messages a second for one line of status.
type transferState struct {
	prog       *sftppkg.Progress
	cancel     context.CancelFunc
	label      string // "app.tar.gz" or "4 items"
	upload     bool
	started    time.Time
	cancelling bool // ctrl+c seen; the goroutine has not stopped yet
}

// progressInterval is how often the status line is redrawn during a transfer.
const progressInterval = 100 * time.Millisecond

// progressTickMsg exists only to make the frame render again — its handler
// changes no state at all, because View reads the counters directly.
type progressTickMsg struct{ gen int }

// plannedMsg is the walk's answer: what a transfer would move, and how much.
type plannedMsg struct {
	gen int
	req transferReq
	err error
}

// deletePlannedMsg is planMsg's counterpart for a delete: the counts that go
// into the confirmation, so a recursive delete is never one keystroke.
type deletePlannedMsg struct {
	gen     int
	side    focusArea
	dir     string
	entries []model.FileEntry
	files   int
	dirs    int
	err     error
}

type transferDoneMsg struct {
	gen    int
	label  string
	result sftppkg.Result
	err    error
}

// fileOpDoneMsg reports a delete or a rename. Both only ever need a status line
// and a refresh, so they share one message.
type fileOpDoneMsg struct {
	gen    int
	status string
	err    error
}

// ── commands ────────────────────────────────────────────────────────────────

func loadServers(store *config.Store) tea.Cmd {
	return func() tea.Msg {
		servers, err := store.Load()
		return serversLoadedMsg{servers: servers, err: err}
	}
}

func loadUIState(store *config.Store) tea.Cmd {
	return func() tea.Msg {
		return uiStateLoadedMsg{state: store.LoadUIState()}
	}
}

func loadKeymap(store *config.Store) tea.Cmd {
	return func() tea.Msg {
		keys, err := store.LoadKeys()
		return keymapLoadedMsg{keys: keys, err: err}
	}
}

// saveUIState persists the fold and sort preferences. A failure is deliberately
// swallowed: this file is view sludge, and a warning about it would be noise on
// top of a session the user actually cares about.
func saveUIState(store *config.Store, st config.UIState) tea.Cmd {
	return func() tea.Msg {
		_ = store.SaveUIState(st)
		return nil
	}
}

func parseSSHConfigCmd(path string) tea.Cmd {
	return func() tea.Msg {
		entries, err := config.ParseSSHConfig(path)
		return sshConfigParsedMsg{path: path, entries: entries, err: err}
	}
}

// importServers appends the chosen entries in one write. Saving per entry would
// rewrite servers.json once per host for no gain.
func importServers(store *config.Store, chosen []model.Server, skipped int) tea.Cmd {
	return func() tea.Msg {
		servers, err := store.AddAll(chosen)
		if err != nil {
			return serversImportedMsg{err: err}
		}
		return serversImportedMsg{
			servers:  servers,
			imported: len(chosen),
			skipped:  skipped,
		}
	}
}

// saveServer persists the entry. Since v6 nothing secret goes to disk here: the
// password and any pasted key body come back on the message and are put into
// the vault by Update, which is the only place allowed to touch it.
func saveServer(store *config.Store, srv model.Server, keyBody string) tea.Cmd {
	password := srv.Password
	// A pasted key satisfies Validate without ever reaching the disk: KeyPEM is
	// json:"-", so this is only ever an in-memory claim that a key exists.
	if keyBody != "" {
		srv.KeyPEM = []byte(keyBody)
	}
	return func() tea.Msg {
		if err := srv.Validate(); err != nil {
			return serverSavedMsg{err: err}
		}
		saved, err := store.Add(srv)
		if err != nil {
			return serverSavedMsg{err: err}
		}
		servers, err := store.Load()
		return serverSavedMsg{
			servers:  servers,
			saved:    saved,
			password: password,
			keyBody:  keyBody,
			err:      err,
		}
	}
}

// updateServer is saveServer's counterpart for an existing entry. A blank key
// body keeps whatever key the entry already had, in the vault or at KeyPath.
func updateServer(store *config.Store, srv model.Server, keyBody string) tea.Cmd {
	password := srv.Password
	// A pasted key satisfies Validate without ever reaching the disk: KeyPEM is
	// json:"-", so this is only ever an in-memory claim that a key exists.
	if keyBody != "" {
		srv.KeyPEM = []byte(keyBody)
	}
	return func() tea.Msg {
		if err := srv.Validate(); err != nil {
			return serverSavedMsg{err: err}
		}
		if err := store.Update(srv); err != nil {
			return serverSavedMsg{err: err}
		}
		servers, err := store.Load()
		return serverSavedMsg{
			servers:  servers,
			saved:    srv,
			password: password,
			keyBody:  keyBody,
			editing:  true,
			err:      err,
		}
	}
}

func removeServer(store *config.Store, id string) tea.Cmd {
	return func() tea.Msg {
		if err := store.Remove(id); err != nil {
			return serversLoadedMsg{err: err}
		}
		servers, err := store.Load()
		return serversLoadedMsg{servers: servers, removed: id, err: err}
	}
}

// connect resolves the known_hosts paths inside the command, not on the UI
// goroutine: KnownHostsFiles stats the filesystem.
func connect(store *config.Store, prompts chan<- *sshpkg.HostKeyPrompt, srv model.Server, gen, cols, rows int) tea.Cmd {
	return func() tea.Msg {
		opts := sshpkg.Options{
			KnownHostsFiles: store.KnownHostsFiles(),
			AppendKnownHost: store.AppendKnownHost,
			Prompts:         prompts,
		}
		sess, err := sshpkg.Connect(srv, cols, rows, opts)
		if err != nil {
			return connectFailedMsg{gen: gen, err: err}
		}
		return connectedMsg{gen: gen, session: sess}
	}
}

// connectSFTP opens the file-transfer connection and resolves the remote home
// directory in one command. Both are network round trips, so neither can happen
// in Update.
func connectSFTP(store *config.Store, prompts chan<- *sshpkg.HostKeyPrompt, srv model.Server, gen int) tea.Cmd {
	return func() tea.Msg {
		opts := sshpkg.Options{
			KnownHostsFiles: store.KnownHostsFiles(),
			AppendKnownHost: store.AppendKnownHost,
			Prompts:         prompts,
		}
		remote, err := sftppkg.Connect(srv, opts)
		if err != nil {
			return sftpFailedMsg{gen: gen, err: err}
		}
		dir, err := remote.Home()
		if err != nil {
			_ = remote.Close()
			return sftpFailedMsg{gen: gen, err: err}
		}
		entries, err := remote.List(dir)
		if err != nil {
			// The connection is fine, the directory is not — keep it and let the
			// pane show the error.
			return sftpConnectedMsg{gen: gen, remote: remote, dir: dir}
		}
		return sftpConnectedMsg{gen: gen, remote: remote, dir: dir, entries: entries}
	}
}

// listDir reads a directory for one pane. Local listings go through here too:
// os.ReadDir is file IO and must not run on the UI goroutine.
func listDir(br sftppkg.Browser, side focusArea, dir string, gen int) tea.Cmd {
	return func() tea.Msg {
		entries, err := br.List(dir)
		return listedMsg{gen: gen, side: side, dir: dir, entries: entries, err: err}
	}
}

// planArgs is what the walk needs. It is a struct because the caller is
// assembling seven values that are easy to swap by accident.
type planArgs struct {
	src, dst sftppkg.Browser
	upload   bool
	srcDir   string
	dstDir   string
	entries  []model.FileEntry
	existing map[string]bool // names already in the destination listing
	gen      int
}

// planTransfer walks every picked root and turns it into a request the
// confirmation panel can describe exactly. It is a command rather than part of
// buildTransfer because walking a remote directory is a network round trip.
func planTransfer(args planArgs) tea.Cmd {
	return func() tea.Msg {
		req := transferReq{
			upload:  args.upload,
			entries: args.entries,
			srcDir:  args.srcDir,
			dstDir:  args.dstDir,
		}
		for _, e := range args.entries {
			root := args.src.Join(args.srcDir, e.Name)
			items, total, skipped, err := sftppkg.Plan(args.src, root)
			if err != nil {
				return plannedMsg{gen: args.gen, err: err}
			}
			req.sets = append(req.sets, sftppkg.Set{
				Upload:  args.upload,
				SrcRoot: root,
				DstRoot: args.dst.Join(args.dstDir, e.Name),
				Items:   items,
				Skipped: skipped,
			})
			req.total += total
			req.skipped += skipped
			for _, it := range items {
				if it.IsDir {
					req.dirs++
				} else {
					req.files++
				}
			}
			if args.existing[e.Name] {
				req.overwrite = append(req.overwrite, e.Name)
			}
		}
		return plannedMsg{gen: args.gen, req: req}
	}
}

// runTransfer performs one confirmed copy. Still one transfer at a time — a
// queue is not v3 — but it now spans several roots, reports progress through
// the shared counter and stops when the context is cancelled.
func runTransfer(ctx context.Context, remote *sftppkg.Remote, req transferReq, prog *sftppkg.Progress, gen int) tea.Cmd {
	return func() tea.Msg {
		var total sftppkg.Result
		for _, set := range req.sets {
			res, err := sftppkg.RunSet(ctx, remote, set, prog)
			total.Files += res.Files
			total.Bytes += res.Bytes
			total.Skipped += res.Skipped
			if err != nil {
				return transferDoneMsg{gen: gen, label: req.label(), result: total, err: err}
			}
		}
		return transferDoneMsg{gen: gen, label: req.label(), result: total}
	}
}

// tickProgress reschedules the redraw while a transfer is running. Nothing
// ticks when the app is idle.
func tickProgress(gen int) tea.Cmd {
	return tea.Tick(progressInterval, func(time.Time) tea.Msg {
		return progressTickMsg{gen: gen}
	})
}

// planDelete counts what a delete would remove. Directories are walked for the
// same reason transfers are: the user has to see the number before saying yes.
func planDelete(br sftppkg.Browser, side focusArea, dir string, entries []model.FileEntry, gen int) tea.Cmd {
	return func() tea.Msg {
		msg := deletePlannedMsg{gen: gen, side: side, dir: dir, entries: entries}
		for _, e := range entries {
			items, _, _, err := sftppkg.Plan(br, br.Join(dir, e.Name))
			if err != nil {
				return deletePlannedMsg{gen: gen, err: err}
			}
			for _, it := range items {
				if it.IsDir {
					msg.dirs++
				} else {
					msg.files++
				}
			}
		}
		return msg
	}
}

// runDelete removes the confirmed entries. It stops at the first failure and
// says so — what has already gone is gone.
func runDelete(br sftppkg.Browser, dir string, entries []model.FileEntry, gen int) tea.Cmd {
	return func() tea.Msg {
		for i, e := range entries {
			if err := br.Remove(br.Join(dir, e.Name), e.IsDir); err != nil {
				return fileOpDoneMsg{
					gen:    gen,
					status: fmt.Sprintf("deleted %d of %d", i, len(entries)),
					err:    err,
				}
			}
		}
		return fileOpDoneMsg{gen: gen, status: "deleted " + plural(len(entries), "item")}
	}
}

func runRename(br sftppkg.Browser, dir, from, to string, gen int) tea.Cmd {
	return func() tea.Msg {
		if err := br.Rename(br.Join(dir, from), br.Join(dir, to)); err != nil {
			return fileOpDoneMsg{gen: gen, err: err}
		}
		return fileOpDoneMsg{gen: gen, status: "renamed to " + to}
	}
}

// waitForHostKey is the fingerprint-prompt pump, rescheduled after each
// message exactly like waitForOutput.
func waitForHostKey(ch <-chan *sshpkg.HostKeyPrompt) tea.Cmd {
	return func() tea.Msg {
		return hostKeyPromptMsg{prompt: <-ch}
	}
}

// waitForOutput is the standard Bubble Tea pump: one channel receive per
// command, rescheduled after each message.
func waitForOutput(sess *sshpkg.Session, gen int) tea.Cmd {
	return func() tea.Msg {
		data, ok := <-sess.Output()
		if !ok {
			return sessionEndedMsg{gen: gen, err: sess.ExitErr()}
		}
		return outputMsg{gen: gen, data: data}
	}
}

// ── update ──────────────────────────────────────────────────────────────────

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return a, a.resize(msg.Width, msg.Height)

	case serversLoadedMsg:
		if msg.err != nil {
			a.errMsg = msg.err.Error()
			return a, nil
		}
		a.servers = msg.servers
		a.sidebar.SetServers(a.servers)
		// A deleted server's secrets go with it. Only re-seal when there was
		// actually something to forget.
		if msg.removed != "" && a.pass != "" && a.hasSecretsFor(msg.removed) {
			a.secrets.Forget(msg.removed)
			return a, a.persistSecrets("")
		}
		return a, nil

	case startupScanMsg:
		if msg.err != nil {
			a.errMsg = msg.err.Error()
			return a, nil
		}
		return a, a.gateStartup(msg)

	case unlockedMsg:
		cmd := a.applyUnlocked(msg)
		// The list on disk may have been rewritten by the migration.
		return a, tea.Batch(cmd, loadServers(a.store))

	case secretsSavedMsg:
		if msg.err != nil {
			a.errMsg = "vault: " + firstLineOf(msg.err)
			return a, nil
		}
		if msg.status != "" {
			a.status = msg.status
		}
		return a, nil

	case uiStateLoadedMsg:
		a.sidebar.SetCollapsed(msg.state.Collapsed)
		a.sidebar.SetSortRecent(msg.state.SortRecent)
		return a, nil

	case keymapLoadedMsg:
		a.applyKeymap(msg)
		return a, nil

	case sshConfigParsedMsg:
		if msg.err != nil {
			a.errMsg = msg.err.Error()
			a.importing, a.rightMode = nil, a.prevRight
			a.focus = focusSidebar
			return a, nil
		}
		if len(msg.entries) == 0 {
			a.importing, a.rightMode = nil, a.prevRight
			a.focus = focusSidebar
			a.status = "no Host blocks in " + msg.path
			return a, nil
		}
		im := newImporter(msg.path, msg.entries, a.servers)
		a.importing = &im
		return a, nil

	case serversImportedMsg:
		a.importing = nil
		a.rightMode = a.prevRight
		a.focus = focusSidebar
		if msg.err != nil {
			a.errMsg = msg.err.Error()
			return a, nil
		}
		a.servers = msg.servers
		a.sidebar.SetServers(a.servers)
		a.status = fmt.Sprintf("imported %d servers (%d skipped)", msg.imported, msg.skipped)
		return a, nil

	case serverSavedMsg:
		if msg.err != nil {
			a.form.err = msg.err.Error()
			return a, nil
		}
		a.servers = msg.servers
		a.sidebar.SetServers(a.servers)
		a.status = "saved"
		if msg.editing {
			a.status = "updated"
		}
		a.rightMode = rightEmpty
		a.focus = focusSidebar

		// The secrets are put in the vault here rather than in the command,
		// because the vault belongs to the model and a command runs elsewhere.
		var cmd tea.Cmd
		if id := msg.saved.ID; id != "" {
			changed := false
			if msg.saved.Auth == model.AuthPassword {
				a.secrets.SetPassword(id, msg.password)
				changed = true
			}
			if msg.keyBody != "" {
				a.secrets.SetKey(id, msg.keyBody)
				changed = true
			}
			if msg.saved.Auth == model.AuthAgent {
				// Switching to the agent means we hold nothing for this server.
				a.secrets.Forget(id)
				changed = true
			}
			if changed && a.pass != "" {
				cmd = a.persistSecrets(a.status)
			}
		}
		return a, cmd

	case connectedMsg:
		t, ok := a.tabByGen(msg.gen)
		if !ok {
			// A newer attempt, or a closed tab, superseded this one.
			_ = msg.session.Close()
			return a, nil
		}
		reconnected := t.attempt > 0
		t.session = msg.session
		t.state = tabLive
		t.attempt = 0
		t.until = time.Time{}
		t.lastErr = nil
		t.scrollOff = 0
		t.clearSelection()
		if reconnected {
			// The remote shell is a new one; the frozen screen belonged to the
			// old. Clearing it here rather than when the connection dropped is
			// what lets the user read it while the reconnect is in flight.
			cols, rows := a.rightInner()
			resetEmulator(t.emu(), cols, rows)
		}
		// Point the input pump at this session. Key events are encoded by the
		// emulator, so cursor-key modes stay correct.
		t.slot.pump.attach(msg.session)
		if t == a.cur() {
			a.rightMode = rightTerminal
			if reconnected {
				a.status = "reconnected · new shell, the old one is gone"
			} else {
				a.focus = focusSession
				a.status = fmt.Sprintf("connected · %s to return to the list", escapeHint)
			}
		} else {
			t.activity = true
		}
		return a, tea.Batch(waitForOutput(msg.session, t.gen), a.markUsed(t.id))

	case connectFailedMsg:
		t, ok := a.tabByGen(msg.gen)
		if !ok {
			return a, nil
		}
		a.confirm = nil
		// A reconnect that fails goes back to waiting rather than to the error
		// card: the tab and its last screen stay, and the backoff carries on.
		if t.attempt > 0 {
			if errors.Is(msg.err, sshpkg.ErrHostKeyUnknown) {
				// The one failure that must not be retried unattended.
				a.stopReconnecting(t, msg.err)
				return a, nil
			}
			return a, a.scheduleReconnect(t, msg.err)
		}
		// A first connect that never came up leaves nothing worth keeping.
		srv := t.srv
		a.closeTab(a.tabIndexOf(t))
		// A locked key is a question, not a failure: the answer is one line of
		// input, so asking beats an error card offering "edit connection".
		if errors.Is(msg.err, sshpkg.ErrKeyPassphraseRequired) {
			a.askKeyPassphrase(srv, false)
			return a, nil
		}
		a.rightMode = rightError
		a.focus = focusSidebar
		a.failErr = msg.err
		a.errMsg = firstLineOf(msg.err)
		return a, nil

	case reconnectMsg:
		t, ok := a.tabByGen(msg.gen)
		if !ok || t.state != tabLost {
			return a, nil
		}
		return a, a.reconnect(t, true)

	case hostKeyPromptMsg:
		return a, tea.Batch(a.askHostKey(msg.prompt), waitForHostKey(a.hostKeys))

	case hostKeyAnsweredMsg:
		a.confirm = nil
		return a, nil

	case clipFlushMsg:
		// The frame that carried the sequence has been written; anything after it
		// must not carry it again.
		a.clip = ""
		return a, nil

	case outputMsg:
		t, ok := a.tabByGen(msg.gen)
		if !ok || t.emu() == nil {
			return a, nil
		}
		// Background tabs are fed exactly like the visible one — that is the
		// whole of "the session keeps running while you look elsewhere".
		_, _ = t.emu().Write(msg.data)
		if t != a.cur() {
			t.activity = true
		}
		return a, waitForOutput(t.session, t.gen)

	case sessionEndedMsg:
		t, ok := a.tabByGen(msg.gen)
		if !ok {
			return a, nil
		}
		// A connection that died under us is worth getting back; a shell that
		// exited on purpose is not, or "exit" would start an endless retry loop.
		if errors.Is(msg.err, sshpkg.ErrConnectionLost) {
			return a, a.scheduleReconnect(t, msg.err)
		}
		a.closeTab(a.tabIndexOf(t))
		if msg.err != nil {
			a.errMsg = "session ended: " + msg.err.Error()
		} else {
			a.status = "session closed"
		}
		return a, nil

	case sftpConnectedMsg:
		if msg.gen != a.sftpGen {
			_ = msg.remote.Close()
			return a, nil
		}
		a.remote = msg.remote
		a.remotePane.br = msg.remote
		a.remotePane.setEntries(msg.dir, msg.entries)
		a.connectingSFTP = ""
		a.status = fmt.Sprintf("sftp ready · tab switches panes · %s to return to the list", escapeHint)
		return a, nil

	case sftpFailedMsg:
		if msg.gen != a.sftpGen {
			return a, nil
		}
		// The panes stay up and the card floats over them: the local side is
		// still worth looking at, and retrying should not cost the layout.
		a.connectingSFTP = ""
		a.confirm = nil
		if errors.Is(msg.err, sshpkg.ErrKeyPassphraseRequired) {
			a.askKeyPassphrase(a.lastAttempt, true)
			return a, nil
		}
		a.sftpErr = msg.err
		a.failErr = msg.err
		a.focus = focusLocal
		a.errMsg = firstLineOf(msg.err)
		return a, nil

	case listedMsg:
		if msg.gen != a.sftpGen {
			return a, nil
		}
		pane := a.pane(msg.side)
		if pane == nil {
			return a, nil
		}
		if msg.err != nil {
			pane.dir = msg.dir
			pane.entries = nil
			pane.cursor, pane.offset = 0, 0
			pane.err = firstLineOf(msg.err)
			return a, nil
		}
		pane.setEntries(msg.dir, msg.entries)
		return a, nil

	case plannedMsg:
		if msg.gen != a.sftpGen || !a.scanning {
			return a, nil
		}
		a.scanning = false
		a.status = ""
		if msg.err != nil {
			a.errMsg = firstLineOf(msg.err)
			return a, nil
		}
		if len(msg.req.sets) == 0 {
			return a, nil
		}
		req := msg.req
		a.pending = &req
		return a, nil

	case deletePlannedMsg:
		if msg.gen != a.sftpGen || !a.scanning {
			return a, nil
		}
		a.scanning = false
		a.status = ""
		if msg.err != nil {
			a.errMsg = firstLineOf(msg.err)
			return a, nil
		}
		a.confirm = a.deleteConfirm(msg)
		return a, nil

	case progressTickMsg:
		// The tick changes nothing: View reads the counters itself, so getting
		// here at all is the point. Rescheduling stops with the transfer.
		if msg.gen != a.sftpGen || a.transfer == nil {
			return a, nil
		}
		return a, tickProgress(a.sftpGen)

	case transferDoneMsg:
		if msg.gen != a.sftpGen {
			return a, nil
		}
		if a.transfer != nil {
			a.transfer.cancel() // releases the context whichever way this ended
			a.transfer = nil
		}
		switch {
		case errors.Is(msg.err, sftppkg.ErrCancelled):
			// Cancelling is an answer, not a failure.
			a.errMsg = ""
			a.status = "transfer cancelled"
		case msg.err != nil:
			a.errMsg = firstLineOf(msg.err)
			a.status = ""
		default:
			a.errMsg = ""
			a.status = fmt.Sprintf("sent %s (%s)", msg.label, humanSize(msg.result.Bytes))
			if msg.result.Skipped > 0 {
				a.status += fmt.Sprintf(" · skipped %d", msg.result.Skipped)
			}
		}
		// Show the files where they landed instead of making the user press r.
		return a, a.refreshPanes()

	case fileOpDoneMsg:
		if msg.gen != a.sftpGen {
			return a, nil
		}
		if msg.err != nil {
			a.errMsg = firstLineOf(msg.err)
		} else {
			a.errMsg = ""
		}
		a.status = msg.status
		return a, a.refreshPanes()

	case syncCheckedMsg:
		if a.syncForm != nil {
			a.syncForm.busy = false
		}
		if msg.err != nil {
			if a.syncForm != nil {
				a.syncForm.err = syncAdvice(msg.err)
			}
			return a, nil
		}
		// Only now, with the repository proven private, is the token stored.
		auth := msg.auth
		if old := a.secrets.GitHub; old != nil && old.Owner == auth.Owner && old.Repo == auth.Repo && old.Path == auth.Path {
			auth.SHA = old.SHA // same file: keep the optimistic lock
		}
		a.secrets.GitHub = &auth
		a.closeSync()
		return a, a.persistSecrets("sync registered · " + auth.Owner + "/" + auth.Repo + " · S push, P pull")

	case syncPushedMsg:
		if msg.err != nil {
			a.status = ""
			a.errMsg = syncAdvice(msg.err)
			return a, nil
		}
		a.errMsg = ""
		if a.secrets.GitHub != nil {
			a.secrets.GitHub.SHA = msg.sha
		}
		status := fmt.Sprintf("synced · %s · %s", plural(msg.servers, "server"), time.Now().Format("2006-01-02 15:04"))
		return a, a.persistSecrets(status)

	case syncFetchedMsg:
		a.status = ""
		if msg.err != nil {
			a.errMsg = syncAdvice(msg.err)
			return a, nil
		}
		a.errMsg = ""
		a.confirm = a.pullConfirm(msg)
		a.focus = focusSidebar
		return a, nil

	case syncAppliedMsg:
		if msg.err != nil {
			a.status = ""
			a.errMsg = syncAdvice(msg.err)
			return a, nil
		}
		a.secrets = msg.secrets
		a.errMsg = ""
		a.status = pullStatus(msg.report)
		if len(msg.report.Conflicts) > 0 {
			// Host keys that disagreed are not a footnote: that is what a
			// machine-in-the-middle looks like, so it gets the warning line.
			a.warning = "⚠ kept the local host key for " + strings.Join(msg.report.Conflicts, ", ")
		}
		// Open tabs are deliberately untouched: their connections are already
		// made, and a list that changed under them says nothing about that.
		return a, loadServers(a.store)

	case tea.MouseMsg:
		// The gate takes the mouse too, or a click could reach a list that is
		// not even drawn.
		if a.unlock != nil {
			return a, nil
		}
		return a, a.handleMouse(msg)

	case tea.KeyMsg:
		return a, a.handleKey(msg)
	}

	// Anything else (spinner ticks, cursor blinks) goes to the focused widget.
	switch a.focus {
	case focusForm:
		cmd, _ := a.form.Update(msg)
		return a, cmd
	case focusSidebar:
		return a, a.sidebar.Update(msg)
	}
	return a, nil
}

// deleteConfirm turns the counted delete into the shared confirmation. The
// numbers are the whole point: a recursive delete must never look like a
// single-file one.
func (a *App) deleteConfirm(msg deletePlannedMsg) *confirm {
	pane := a.pane(msg.side)
	if pane == nil || pane.br == nil || len(msg.entries) == 0 {
		return nil
	}

	head := fmt.Sprintf("Delete %s?", plural(len(msg.entries), "item"))
	if len(msg.entries) == 1 {
		head = fmt.Sprintf("Delete %s?", msg.entries[0].Name)
	}
	detail := plural(msg.files, "file")
	if msg.dirs > 0 {
		detail = plural(msg.dirs, "directory") + ", " + detail
	}

	warn := ""
	if msg.dirs > 0 {
		warn = "⚠ directories are deleted with everything inside them"
	}

	return &confirm{
		title:  "Delete",
		lines:  []string{head, "", detail, "in  " + msg.dir},
		warn:   warn,
		accept: "[enter] delete",
		onYes:  runDelete(pane.br, msg.dir, msg.entries, a.sftpGen),
	}
}

func (a *App) handleKey(msg tea.KeyMsg) tea.Cmd {
	// The vault gate is the strictest form of the modal rule: while it is up
	// nothing else is drawn and nothing else may see a key — not q, not alt+2,
	// not ctrl+b. There is no state behind it to reach.
	if a.unlock != nil {
		return a.handleUnlockKey(msg)
	}

	// A key passphrase question is the same kind of thing, one layer down.
	if a.keyPass != nil {
		return a.handleKeyPassKey(msg)
	}

	// The help card is a modal too — the lightest one, since any key it does not
	// use closes it. It never opens over another dialog, so nothing is stacked
	// under this branch.
	if a.help != nil {
		return a.handleHelpKey(msg)
	}

	// ctrl+c means "stop the transfer", not "quit", while one is running. The
	// session branch below still gets it when the shell is focused: a remote
	// program needs its own interrupt.
	if a.transfer != nil && a.focus != focusSession && msg.Type == tea.KeyCtrlC {
		a.cancelTransfer()
		return nil
	}

	// The filter is the newest question on screen, so it owns every key while it
	// is being typed — q and ctrl+c included. A search you cannot type "q" into
	// is not a search; esc closes it and the shortcuts come back. This is the
	// same "a dialog swallows the keyboard" rule confirm and pending follow,
	// applied to the list.
	if a.focus == focusSidebar && a.sidebar.Filtering() {
		return a.sidebar.Update(msg)
	}

	// Tab switching works from anywhere, session focus included: alt combinations
	// are ours, everything else the shell may still need. tabKey stands down
	// while a dialog is up, like every other binding.
	if cmd, handled := a.tabKey(msg); handled {
		return cmd
	}

	// The import preview is modal for the same reason. So is the sync form: it
	// is a set of fields being filled in, and switching the panel out from under
	// it would strand them.
	if a.focus == focusImport {
		return a.handleImportKey(msg)
	}
	if a.focus == focusSync {
		return a.handleSyncKey(msg)
	}

	// The rename input owns the keyboard the same way a confirmation does, and
	// for the same reason — it is the newest question on screen.
	if a.rename != nil {
		return a.resolveRename(msg)
	}

	// A confirmation owns the keyboard while it is up. Unhandled keys are
	// dropped rather than forwarded, so nothing leaks into the session behind it.
	if a.confirm != nil {
		cmd, handled := a.confirm.resolve(a.keys, msg)
		if handled {
			a.confirm = nil
		}
		return cmd
	}

	// A transfer awaiting confirmation owns the keyboard the same way, for the
	// same reason: nothing may reach the panes behind it.
	if a.pending != nil {
		return a.resolvePending(msg)
	}

	// ? opens the shortcut card — but only where the key is ours to take. In a
	// session it belongs to the shell, in a text field it is a character, and
	// over a dialog nothing may be stacked. helpAvailable is the same question
	// the status line asks before it advertises the key.
	if a.helpAvailable() && a.keys.Action(ctxGlobal, msg.String()) == actHelp {
		a.openHelp()
		return nil
	}

	// Session focus swallows everything except the escape key: the remote shell
	// needs ctrl+c, ctrl+d, q and friends.
	if a.focus == focusSession {
		act := a.keys.Action(ctxSession, msg.String())
		if act == actEscape {
			a.focus = focusSidebar
			a.status = "session still running · select it again to go back"
			return nil
		}
		if a.scrollKey(act) {
			return nil
		}
		// A tab with no session behind it has nowhere to send keys, so r takes
		// its usual meaning here: try again now, without waiting out the backoff.
		if t := a.cur(); t != nil && t.down() {
			if act == actReconnect && t.state == tabLost {
				return a.reconnect(t, false)
			}
			return nil
		}
		// Any other key means "I am done reading history" — and done with the
		// selection, which is about to point at whatever the shell prints next.
		if t := a.cur(); t != nil {
			t.scrollOff = 0
			t.clearSelection()
		}
		a.sendKey(msg)
		return nil
	}

	switch a.focus {
	case focusLocal, focusRemote:
		return a.handleSFTPKey(msg)

	case focusForm:
		cmd, action := a.form.Update(msg)
		switch action {
		case formCancel:
			a.rightMode = rightEmpty
			a.focus = focusSidebar
			return nil
		case formSubmit:
			srv, keyBody, err := a.form.Server()
			if err != nil {
				a.form.err = err.Error()
				return nil
			}
			if err := srv.Validate(); err != nil && keyBody == "" {
				a.form.err = err.Error()
				return nil
			}
			a.form.err = ""
			return a.submitForm(srv, keyBody)
		}
		return cmd

	default: // focusSidebar
		// The error card offers its own actions; they take precedence over the
		// list's while it is showing.
		if a.rightMode == rightError {
			switch a.keys.Action(ctxError, msg.String()) {
			case actErrorRetry:
				return a.retryConnect()
			case actErrorEdit:
				return a.editServer(a.lastAttempt)
			case actErrorDismiss:
				a.dismissError()
				return nil
			}
		}

		if a.keys.Action(ctxGlobal, msg.String()) == actQuit {
			a.quitting = true
			a.teardownAllSessions()
			a.teardownSFTP() // cancels a running transfer first
			return tea.Quit
		}

		switch a.keys.Action(ctxSidebar, msg.String()) {
		case actConnect:
			return a.activateSelection()
		case actToggleGroup:
			// Only meaningful on a header; on a server row the list keeps its
			// own handling of the key.
			if cmd, ok := a.toggleGroup(); ok {
				return cmd
			}
		case actFoldGroup, actUnfoldGroup:
			// A direction says which way to fold, unlike the toggles above.
			if it, ok := a.sidebar.Selected(); ok && it.header {
				fold := a.keys.Action(ctxSidebar, msg.String()) == actFoldGroup
				if a.sidebar.SetGroupCollapsed(fold) {
					return a.persistUIState()
				}
				return nil
			}
		case actImport:
			return a.openImport()
		case actSyncSetup:
			return a.openSync()
		case actSyncPush:
			return a.startPush()
		case actSyncPull:
			return a.startPull()
		case actSortRecent:
			a.sidebar.SetSortRecent(!a.sidebar.SortRecent())
			if a.sidebar.SortRecent() {
				a.status = "sorted by last used"
			} else {
				a.status = "sorted by saved order"
			}
			return a.persistUIState()
		case actNewSession:
			// A second session to a server that already has one. enter reuses
			// the open tab, so this is the only way to ask for another.
			if it, ok := a.sidebar.Selected(); ok && a.isServerRow(it) {
				return a.openTab(it.server, true)
			}
			return nil
		case actOpenFiles:
			return a.openSFTP()
		case actEditServer:
			if it, ok := a.sidebar.Selected(); ok && a.isServerRow(it) {
				return a.editServer(it.server)
			}
			return nil
		case actDeleteEntry:
			return a.deleteSelection()
		case actFocusPanel:
			if a.rightMode == rightTerminal && a.curSession() != nil {
				a.focus = focusSession
				return nil
			}
			if a.rightMode == rightForm {
				a.focus = focusForm
				return nil
			}
			if a.rightMode == rightSFTP {
				a.focus = focusLocal
				return nil
			}
			return nil
		}
		return a.sidebar.Update(msg)
	}
}

// submitForm saves the entry, creating the vault first if this is the first
// secret the user has ever stored.
//
// The vault is made here and nowhere earlier: that is the whole of the "no
// secrets, no prompt" rule. Somebody who only ever uses ssh-agent goes through
// this function every time and is never asked for a passphrase.
func (a *App) submitForm(srv model.Server, keyBody string) tea.Cmd {
	save := func(app *App) tea.Cmd {
		if app.form.editingID != "" {
			return updateServer(app.store, srv, keyBody)
		}
		return saveServer(app.store, srv, keyBody)
	}

	hasSecret := (srv.Auth == model.AuthPassword && srv.Password != "") || keyBody != ""
	if hasSecret {
		if cmd, waiting := a.requireVault(
			"This password will be encrypted.\nChoose a passphrase for the vault — it is never stored.", save); waiting {
			return cmd
		}
	}
	return save(a)
}

// activateSelection opens the form or connects, depending on the highlighted
// row.
func (a *App) activateSelection() tea.Cmd {
	it, ok := a.sidebar.Selected()
	if !ok {
		return nil
	}
	// A group header folds instead of connecting: there is no session behind it.
	if it.header {
		if cmd, ok := a.toggleGroup(); ok {
			return cmd
		}
		return nil
	}

	a.errMsg = ""
	a.status = ""

	if it.connect {
		w, h := a.rightInner()
		a.form = newForm(w, h)
		a.form.setGroups(a.sidebar.Groups())
		a.rightMode = rightForm
		a.focus = focusForm
		return nil
	}

	// A server that already has a tab is shown rather than dialled again; n is
	// how you ask for a second session to the same host.
	return a.openTab(it.server, false)
}

// markUsed stamps a server as most recently connected, for the recent-first
// sort. Only a session that actually came up counts — a failed dial says
// nothing about which host you work on.
//
// The in-memory copy is updated straight away so the sidebar reorders now; the
// write is best-effort, because losing a sort key is not worth an error card.
func (a *App) markUsed(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	now := time.Now()
	for i := range a.servers {
		if a.servers[i].ID != id {
			continue
		}
		a.servers[i].LastUsed = now
		srv := a.servers[i]
		a.sidebar.SetServers(a.servers)
		return func() tea.Msg {
			_ = a.store.Update(srv)
			return nil
		}
	}
	return nil
}

// tabIndexOf locates a tab by identity, since messages carry the tab itself.
func (a *App) tabIndexOf(t *sessionTab) int {
	for i, other := range a.tabs {
		if other == t {
			return i
		}
	}
	return -1
}

// retryConnect re-runs the attempt the error card is about, picking up any edit
// made in between.
func (a *App) retryConnect() tea.Cmd {
	srv := a.lastAttempt
	for _, s := range a.servers {
		if s.ID == srv.ID {
			srv = s
			break
		}
	}
	if srv.Host == "" {
		return nil
	}
	if a.lastWasSFTP {
		return a.startSFTP(srv)
	}
	return a.openTab(srv, false)
}

// dismissError closes the card. Other sessions may still be open behind it, in
// which case the panel goes back to showing one rather than to the placeholder.
func (a *App) dismissError() {
	a.rightMode = rightEmpty
	a.failErr = nil
	a.errMsg = ""
	a.focus = focusSidebar
	if len(a.tabs) > 0 {
		a.rightMode = rightTerminal
	}
}

// isServerRow reports whether a row is an actual server, i.e. whether the
// server-targeted shortcuts (n/e/d/f) mean anything on it. The connect action
// and group headers are not.
func (a *App) isServerRow(it item) bool { return !it.connect && !it.header }

// toggleGroup folds the highlighted header and persists the change, reporting
// whether the cursor was on one at all.
func (a *App) toggleGroup() (tea.Cmd, bool) {
	if !a.sidebar.ToggleGroup() {
		return nil, false
	}
	return a.persistUIState(), true
}

// applyKeymap overlays keys.json on the defaults and keeps whatever it had to
// refuse. The problems are said once on the status line and then live in the
// help card, which is the only place they stay readable: a user who rebound a
// key and did not get it needs to be told, and told where to look.
func (a *App) applyKeymap(msg keymapLoadedMsg) {
	a.keys = DefaultKeymap()
	a.keyProblems = nil

	if msg.err != nil {
		a.keyProblems = []KeymapProblem{{Action: "keys.json", Reason: firstLineOf(msg.err)}}
	} else {
		a.keyProblems = a.keys.Apply(msg.keys)
	}
	if len(a.keyProblems) > 0 && a.status == "" {
		a.status = fmt.Sprintf("keys.json: %s — press %s for details, defaults kept",
			plural(len(a.keyProblems), "problem"), a.keys.Key(ctxGlobal, actHelp))
	}
}

// persistUIState mirrors the sidebar's view preferences to ui.json.
func (a *App) persistUIState() tea.Cmd {
	return saveUIState(a.store, config.UIState{
		Collapsed:  a.sidebar.CollapsedGroups(),
		SortRecent: a.sidebar.SortRecent(),
	})
}

// openImport shows the ~/.ssh/config preview. Nothing is read here — parsing is
// file IO, so it goes through a command like every other blocking call.
func (a *App) openImport() tea.Cmd {
	path := config.DefaultSSHConfigPath()
	if path == "" {
		a.status = "no ~/.ssh/config"
		return nil
	}
	im := importer{path: path}
	a.importing = &im
	a.prevRight = a.rightMode
	a.rightMode = rightImport
	a.focus = focusImport
	a.errMsg = ""
	a.status = ""
	return parseSSHConfigCmd(path)
}

// closeImport returns to whatever the right panel was showing before, leaving
// the session tabs untouched.
func (a *App) closeImport() {
	a.importing = nil
	a.rightMode = a.prevRight
	a.focus = focusSidebar
}

// handleImportKey owns every key while the preview is up.
func (a *App) handleImportKey(msg tea.KeyMsg) tea.Cmd {
	im := a.importing
	if im == nil {
		a.focus = focusSidebar
		return nil
	}
	switch a.keys.Action(ctxImport, msg.String()) {
	case actImportCancel:
		a.closeImport()
		a.status = "import cancelled"
	case actImportUp:
		im.move(-1)
	case actImportDown:
		im.move(1)
	case actImportToggle:
		im.toggle()
	case actImportToggleAll:
		im.toggleAll()
	case actImportAccept:
		chosen := im.selected()
		if len(chosen) == 0 {
			a.closeImport()
			a.status = "nothing selected"
			return nil
		}
		return importServers(a.store, chosen, im.skipped())
	}
	return nil
}

func (a *App) editServer(srv model.Server) tea.Cmd {
	if srv.ID == "" {
		return nil
	}
	w, h := a.rightInner()
	// The entry on disk has no password: fill it in from the vault, or the edit
	// would save it back empty.
	a.form = newFormFor(config.Inject(srv, a.secrets), w, h)
	a.form.setGroups(a.sidebar.Groups())
	a.rightMode = rightForm
	a.focus = focusForm
	a.errMsg = ""
	a.failErr = nil
	return nil
}

// deleteSelection asks first. Deleting also removes keys/<id>.pem, which is not
// something to do on a single keystroke.
func (a *App) deleteSelection() tea.Cmd {
	it, ok := a.sidebar.Selected()
	if !ok || !a.isServerRow(it) {
		return nil
	}
	srv := it.server

	lines := []string{
		fmt.Sprintf("Delete %s (%s@%s)?", srv.Title(), srv.User, srv.Addr()),
	}
	warn := ""
	if srv.Auth == model.AuthKey && a.store.OwnsKey(srv.KeyPath) {
		warn = "Its private key " + srv.KeyPath + " will be deleted too."
	}

	a.confirm = &confirm{
		title:  "Delete connection",
		lines:  lines,
		warn:   warn,
		accept: "[enter] delete",
		onYes:  removeServer(a.store, srv.ID),
	}
	a.focus = focusSidebar
	return nil
}

// askHostKey turns a trust-on-first-use question into the confirm panel. There
// is no "connect anyway" for a *changed* key — that case never reaches here,
// the ssh package refuses it outright.
func (a *App) askHostKey(p *sshpkg.HostKeyPrompt) tea.Cmd {
	if !a.dialing() {
		// Nothing is waiting on this any more (the connect was superseded).
		return func() tea.Msg { p.Reject(); return nil }
	}

	a.confirm = &confirm{
		title: "Unknown host",
		lines: []string{
			p.Addr,
			strings.ToUpper(strings.TrimPrefix(p.KeyType, "ssh-")) + "  " + p.Fingerprint,
			"",
			"This host has not been seen before. Verify the fingerprint",
			"against the server itself before trusting it.",
			"Approving stores it in " + a.store.KnownHostsPath() + ".",
		},
		accept: "[enter] trust and connect",
		onYes:  func() tea.Msg { p.Accept(); return hostKeyAnsweredMsg{} },
		onNo:   func() tea.Msg { p.Reject(); return hostKeyAnsweredMsg{} },
	}
	a.focus = focusSidebar
	return nil
}

// scrollKey handles the scrollback bindings, reporting whether it consumed the
// key. Terminals normally eat shift+pgup themselves, and Bubble Tea v1 has no
// key type for it, so shift+up/down is the binding that actually works here.
func (a *App) scrollKey(act Action) bool {
	_, rows := a.rightInner()
	switch act {
	case actScrollUp:
		a.scrollBy(scrollStep)
	case actScrollDown:
		a.scrollBy(-scrollStep)
	case actScrollPageUp:
		a.scrollBy(maxInt(rows/2, 1))
	case actScrollPageDown:
		a.scrollBy(-maxInt(rows/2, 1))
	default:
		return false
	}
	return true
}

// scrollBy moves the viewport, clamped to what the scrollback actually holds.
// The offset belongs to the tab, so switching away and back keeps your place.
func (a *App) scrollBy(delta int) {
	t := a.cur()
	if t == nil {
		return
	}
	t.scrollOff = clampInt(t.scrollOff+delta, 0, maxScrollOffset(t.emu()))
	// The rows the selection pointed at now hold different text, so it goes
	// rather than being dragged along behind the viewport.
	t.clearSelection()
}

// scrollOffset is the visible tab's offset, or 0 when there is no tab.
func (a *App) scrollOffset() int {
	if t := a.cur(); t != nil {
		return t.scrollOff
	}
	return 0
}

// sendKey pushes a key into the emulator, which encodes it and writes the bytes
// to the session through the pipe drained in connectedMsg.
func (a *App) sendKey(msg tea.KeyMsg) {
	t := a.cur()
	if t == nil || t.emu() == nil || t.session == nil {
		return
	}
	if msg.Paste {
		t.emu().SendText(string(msg.Runes))
		return
	}
	if key, ok := keyToVT(msg); ok {
		t.emu().SendKey(key)
	}
}

func (a *App) handleMouse(msg tea.MouseMsg) tea.Cmd {
	// The card blocks the mouse the same way it blocks the keyboard: a click
	// through it would move focus behind a dialog the user can still see.
	if a.help != nil {
		a.helpMouse(msg)
		return nil
	}
	if a.wheelOverTerminal(msg) {
		return nil
	}
	// A session takes press, motion and release the same way the split view does,
	// and for the same three-stage reason — here the drag selects text rather
	// than moving files. The two modes cannot collide: rightMode tells them apart.
	if a.rightMode == rightTerminal && !a.modalUp() {
		if cmd, handled := a.handleSessionMouse(msg); handled {
			return cmd
		}
	}
	// The split view owns press, motion and release while it is up — that three
	// step sequence is the drag. A dialog floating over the panes blocks it, the
	// same way it blocks the keyboard.
	if a.rightMode == rightSFTP && a.confirm == nil && a.pending == nil && a.rename == nil && a.sftpErr == nil {
		if cmd, handled := a.handleSFTPMouse(msg); handled {
			return cmd
		}
	}
	if msg.Action != tea.MouseActionPress || msg.Button != tea.MouseButtonLeft {
		if a.focus == focusSidebar {
			return a.sidebar.Update(msg)
		}
		return nil
	}

	// The panel under the pointer takes focus.
	if msg.X < sidebarWidth {
		a.focus = focusSidebar
		// Rows inside the list start below the border and the title block.
		if idx, ok := a.rowToIndex(msg.Y); ok {
			a.sidebar.list.Select(idx)
			return a.activateSelection()
		}
		return a.sidebar.Update(msg)
	}

	switch a.rightMode {
	case rightForm:
		a.focus = focusForm
		// Translate the click into the form's own coordinates: past the sidebar,
		// the panel border, its title bar and the inner padding.
		local := msg
		local.X -= sidebarWidth + 1 + padX
		local.Y -= topMargin + 1 + rightHeaderRows
		cmd, _ := a.form.Update(local)
		return cmd
	case rightTerminal:
		if a.curSession() != nil {
			a.focus = focusSession
		}
	}
	return nil
}

// wheelOverTerminal scrolls the session panel, reporting whether it consumed
// the event.
//
// On the alternate screen (vim, less) the wheel becomes arrow keys instead:
// vt's scrollback belongs to the main screen, so scrolling it there would show
// unrelated history. That arrow translation is what a real terminal does for
// full-screen apps that have not enabled mouse reporting.
func (a *App) wheelOverTerminal(msg tea.MouseMsg) bool {
	up := msg.Button == tea.MouseButtonWheelUp
	if !up && msg.Button != tea.MouseButtonWheelDown {
		return false
	}
	t := a.cur()
	if a.confirm != nil || a.rightMode != rightTerminal || t == nil || t.emu() == nil {
		return false
	}
	if msg.X < sidebarWidth || t.state == tabConnecting {
		return false
	}

	if t.emu().IsAltScreen() {
		// The alt screen has no scrollback to lift the viewport into, so the wheel
		// becomes arrow keys — and the screen under the selection is about to be
		// redrawn by whatever they do.
		t.clearSelection()
		altScreenScroll(t.emu(), up)
		return true
	}
	if up {
		a.scrollBy(scrollStep)
	} else {
		a.scrollBy(-scrollStep)
	}
	return true
}

// handleSessionMouse implements the three stages of a text selection, reporting
// whether it consumed the event. Dragging over the session panel is the only way
// left to grab the words on screen: turning mouse reporting on in v0 took the
// terminal's own selection away from us, so this is paying that back.
//
// Which stage is which is decided by whether a drag was in flight, never by the
// button value — terminals disagree about what a release reports (v3's lesson,
// learned on the file panes).
func (a *App) handleSessionMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	t := a.cur()
	if t == nil || t.emu() == nil || t.state == tabConnecting {
		return nil, false
	}

	switch msg.Action {
	case tea.MouseActionPress:
		if msg.Button != tea.MouseButtonLeft {
			return nil, false
		}
		p, in := a.sessionCell(msg.X, msg.Y)
		if !in {
			return nil, false // the sidebar, or the frame around us
		}
		// The panel under the pointer takes focus, exactly as a plain click did
		// before there was anything to select.
		if t.session != nil || t.down() {
			a.focus = focusSession
		}
		t.sel = &selection{anchor: p, cursor: p, active: true}
		return nil, true

	case tea.MouseActionMotion:
		if t.sel == nil || !t.sel.active {
			return nil, false
		}
		// Clamped to the panel rather than scrolling it: the viewport moving is
		// what clears a selection, so auto-scroll would erase the drag in progress.
		t.sel.cursor = a.clampSessionCell(msg.X, msg.Y)
		return nil, true

	case tea.MouseActionRelease:
		if t.sel == nil || !t.sel.active {
			return nil, false
		}
		t.sel.active = false
		sel := *t.sel
		if sel.empty() {
			// A click is not a selection; it moved focus and that is all.
			t.sel = nil
			return nil, true
		}
		return a.copySelection(t, sel), true
	}
	return nil, false
}

// sessionCell maps a screen position to a cell in the session body, reporting
// whether it landed inside. The origin is the same one the form's click
// translation uses, minus the padding the terminal grid deliberately has none of.
func (a *App) sessionCell(x, y int) (point, bool) {
	cols, rows := a.rightInner()
	p := point{x: x - sidebarWidth - 1, y: y - topMargin - 1 - rightHeaderRows}
	if p.x < 0 || p.y < 0 || p.x >= cols || p.y >= rows {
		return point{}, false
	}
	return p, true
}

// clampSessionCell is sessionCell for a drag that has left the panel: the
// selection stops at the edge instead of following the pointer out of it.
func (a *App) clampSessionCell(x, y int) point {
	cols, rows := a.rightInner()
	return point{
		x: clampInt(x-sidebarWidth-1, 0, cols-1),
		y: clampInt(y-topMargin-1-rightHeaderRows, 0, rows-1),
	}
}

// clipLimit caps what one copy may put on the clipboard. OSC 52 is a single
// escape sequence: a megabyte of it arrives as one unbroken line and stalls the
// terminal that has to decode it.
const clipLimit = 64 << 10

// copySelection puts the selected text on the system clipboard. Copying happens
// on release rather than on a following keypress because inside a session every
// key belongs to the shell — an X11-style select-to-copy is the only shape that
// does not break the v0 rule.
func (a *App) copySelection(t *sessionTab, sel selection) tea.Cmd {
	cols, rows := a.rightInner()
	text := selectedText(t.emu(), cols, rows, t.scrollOff, &sel)
	if strings.TrimSpace(text) == "" {
		a.status = "nothing to copy"
		return nil
	}

	text, cut := truncateClip(text)
	// The sequence goes out as a prefix on the next frame; the flush message
	// removes it again, so it exists in exactly one frame and never twice.
	a.clip = ansi.SetSystemClipboard(text)
	if cut {
		a.status = fmt.Sprintf("copied %s (truncated)", humanSize(int64(len(text))))
	} else {
		a.status = "copied " + plural(strings.Count(text, "\n")+1, "line")
	}
	a.errMsg = ""
	return func() tea.Msg { return clipFlushMsg{} }
}

// truncateClip cuts the text to clipLimit bytes without splitting a rune, and
// says whether it had to.
func truncateClip(s string) (string, bool) {
	if len(s) <= clipLimit {
		return s, false
	}
	cut := s[:clipLimit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

// listHeaderRows is how far the first list item sits below the top of the
// screen: the margin, the panel border, and the list's own title block (title
// plus the blank line under it). sidebarRowsPerItem is rowDelegate's height —
// one, since v5, which is why a screen row now maps straight to an item.
// TestSidebarRowGeometry pins both.
const (
	listHeaderRows     = topMargin + 1 + 2
	sidebarRowsPerItem = 1
)

// rowToIndex maps a screen row inside the sidebar to a list index.
//
// It counts rendered rows, not servers: group headers are items too, and a
// collapsed group's members are not items at all.
func (a *App) rowToIndex(y int) (int, bool) {
	rel := y - listHeaderRows
	if rel < 0 {
		return 0, false
	}
	idx := rel/sidebarRowsPerItem + a.sidebar.list.Paginator.Page*a.sidebar.list.Paginator.PerPage
	if idx >= len(a.sidebar.list.VisibleItems()) {
		return 0, false
	}
	return idx, true
}

// resize recomputes the layout, the emulator geometry and the remote PTY size.
// All three must move together or the panel and the remote disagree.
func (a *App) resize(width, height int) tea.Cmd {
	// Some terminals report a zero size on the first message; fall back to the
	// classic 80x24 so we still draw something usable.
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	a.width, a.height = width, height

	a.sidebar.SetSize(sidebarWidth-borderSize-2*sidePadX, a.panelHeight())
	cols, rows := a.rightInner()
	a.form.setSize(cols-2*padX, rows)

	// The panes share the terminal body's height budget, so all three panels
	// still close on the same row.
	a.local.rows, a.remotePane.rows = rows, rows
	a.local.clampOffset()
	a.remotePane.clampOffset()

	// Every tab is resized, not just the visible one: a background session that
	// kept the old geometry would be redrawn wrong the moment you switch to it.
	var cmds []tea.Cmd
	for _, t := range a.tabs {
		if emu := t.emu(); emu != nil {
			emu.Resize(cols, rows)
		}
		// Reflowed history no longer lines up with the old offset, so drop back
		// to the live screen rather than showing a scrambled past. The selection
		// goes with it, for the same reason.
		t.scrollOff = 0
		t.clearSelection()
		if sess := t.session; sess != nil {
			cmds = append(cmds, func() tea.Msg {
				_ = sess.Resize(cols, rows)
				return nil
			})
		}
	}
	return tea.Batch(cmds...)
}

// The vertical budget is fixed: a top margin, the two panels, and one status
// row. Both panels get the same content height so their borders line up.
//
//	row 0            top margin
//	rows 1..h-2      sidebar and right panel (identical height)
//	row h-1          status line
const (
	topMargin  = 1
	statusRows = 1
	borderSize = 2 // one border row/column on each side
)

// panelHeight is the content height shared by both panels, borders excluded.
func (a *App) panelHeight() int {
	return maxInt(a.height-topMargin-statusRows-borderSize, 1)
}

// rightInner is the usable cell size of the right panel's body, borders and
// title bar excluded. It is also the emulator and remote PTY size.
func (a *App) rightInner() (cols, rows int) {
	cols = a.width - sidebarWidth - borderSize
	return maxInt(cols, 1), maxInt(a.panelHeight()-rightHeaderRows, 1)
}

// teardownSFTP closes the file connection. It is independent of the terminal
// session on purpose: the two are separate TCP connections.
func (a *App) teardownSFTP() {
	// Stop the copy before the connection under it goes: the goroutine must not
	// outlive the app it is reporting to.
	if a.transfer != nil {
		a.transfer.cancel()
		a.transfer = nil
	}
	if a.remote != nil {
		_ = a.remote.Close()
		a.remote = nil
	}
	a.remotePane = filePane{rows: a.remotePane.rows}
	a.sftpID = ""
	a.sftpName, a.sftpAddr = "", ""
	a.drag, a.pending, a.rename = nil, nil, nil
	a.scanning = false
	a.sftpErr = nil
	a.sftpGen++
}

// ── view ────────────────────────────────────────────────────────────────────

func (a *App) View() string {
	if a.quitting {
		return ""
	}
	if a.width == 0 || a.height == 0 {
		return "starting…"
	}
	// Nothing is drawn behind the gate — not even the server names, which are
	// exactly what the vault exists to keep unreadable.
	if a.unlock != nil {
		return a.unlockView()
	}

	cols, rows := a.rightInner()

	// Both panels are clamped to the exact same rectangle before the border is
	// applied. lipgloss only pads to the requested size, so content that runs
	// long would otherwise push one panel below the other.
	sideBody := lipgloss.NewStyle().Padding(0, sidePadX).Render(a.sidebar.View())
	left := panelStyle(a.focus == focusSidebar).
		Render(clampBlock(sideBody, sidebarWidth-borderSize, a.panelHeight()))

	// The split view replaces the single right panel with two. Its dialogs float
	// over the panes rather than replacing them, so this branch does not care
	// whether one is up.
	right := ""
	if a.rightMode == rightSFTP {
		right = a.sftpPanels(rows)
	} else {
		// The right panel gets a title bar of its own, mirroring the sidebar's
		// "Servers" heading, so a live session always announces which host it is.
		rightContent := a.rightHeader(cols) + "\n" + clampBlock(a.rightBody(cols, rows), cols, rows)
		right = panelStyle(a.focus == focusSession || a.focus == focusForm || a.focus == focusImport || a.focus == focusSync).
			Render(clampBlock(rightContent, cols, a.panelHeight()))
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)

	// Every row is padded to the full width so the whole frame is one clean
	// rectangle, margin row included.
	margin := strings.Repeat(padLine("", a.width)+"\n", topMargin)
	screen := margin + body + "\n" + padLine(a.statusLine(), a.width)

	// The split view's dialogs float over the frame; so does the help card, from
	// any mode. It is the first general use of overlay, and it follows the same
	// width rules — a box that will not fit is not drawn at all.
	if a.rightMode == rightSFTP {
		if box, x, y := a.sftpModal(); box != "" {
			screen = overlay(screen, box, x, y)
		}
	}
	if a.help != nil && a.helpFits() {
		if box, x, y := a.helpModal(); box != "" {
			screen = overlay(screen, box, x, y)
		}
	}

	// A pending clipboard write rides out as a prefix. OSC 52 is zero columns
	// wide, so it passes through padLine and the layout invariants untouched —
	// and writing to stdout ourselves would land in the middle of a frame the
	// renderer owns.
	if a.clip != "" {
		screen = a.clip + screen
	}
	return screen
}

// ansiReset closes whatever styling a spliced fragment left open, so the piece
// after it starts from a known state.
const ansiReset = "\x1b[0m"

// overlay splices box into base at column x, row y, leaving every row exactly
// as wide as it was.
//
// This is the one place that cuts a row that already carries ANSI styling, and
// it is why the split view can float a dialog instead of replacing a panel.
// Each row is rebuilt as left | reset | box | reset | right, where the cuts come
// from ansi.Truncate and ansi.TruncateLeft — both of which carry the accumulated
// SGR state across the cut, so the part after the box keeps its colours. Both
// pieces are then re-padded to their exact widths, because a double-width
// grapheme straddling a cut leaves the fragment one cell short.
func overlay(base, box string, x, y int) string {
	lines := strings.Split(base, "\n")
	boxLines := strings.Split(box, "\n")

	for i, bl := range boxLines {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		total := ansi.StringWidth(lines[row])
		bw := ansi.StringWidth(bl)
		if x < 0 || bw <= 0 || x+bw > total {
			continue
		}
		left := padLine(ansi.Truncate(lines[row], x, "")+ansiReset, x)
		right := padLine(ansi.TruncateLeft(lines[row], x+bw, ""), total-x-bw)
		lines[row] = left + ansiReset + bl + ansiReset + right
	}
	return strings.Join(lines, "\n")
}

// clampBlock forces a block of styled text to exactly w columns by h rows.
func clampBlock(s string, w, h int) string {
	lines := strings.Split(s, "\n")
	out := make([]string, h)
	for i := range out {
		var line string
		if i < len(lines) {
			line = lines[i]
		}
		out[i] = padLine(line, w)
	}
	return strings.Join(out, "\n")
}

// padX insets the form and placeholder panels from the border. The terminal
// grid is deliberately excluded: it must fill the panel exactly or it stops
// matching the remote PTY size.
const (
	padX = 2
	// sidePadX keeps the list's selection bar off the sidebar border.
	sidePadX = 1
	// rightHeaderRows is the title bar plus the blank line under it, matching
	// how the sidebar list draws its own heading.
	rightHeaderRows = 2
)

// rightHeader renders the panel's title bar. In terminal mode it names the
// session, which is the whole point: the body should always say what you are
// looking at.
func (a *App) rightHeader(cols int) string {
	title, detail := "ssh-client", ""

	switch a.rightMode {
	case rightForm:
		title = "New connection"
		if a.form.editingID != "" {
			title = "Edit connection"
		}
	case rightTerminal:
		t := a.cur()
		if t == nil {
			break
		}
		title = t.label()
		switch {
		case t.state == tabConnecting:
			detail = "connecting…"
		case t.state == tabReconnecting:
			detail = "reconnecting…"
		case t.state == tabLost:
			detail = "connection lost"
		case t.scrollOff > 0:
			// Say so loudly: the panel is showing the past, not the live screen.
			detail = fmt.Sprintf("SCROLL −%d", t.scrollOff)
		default:
			detail = t.addr
		}
		// More than one session: the title row becomes the tab strip. It still
		// costs exactly one row, so the body height is unchanged.
		if len(a.tabs) > 1 {
			line := strings.Repeat(" ", padX) + a.tabStrip(cols-2*padX)
			if detail != "" {
				line += styleTitleDetail.Render("  " + detail)
			}
			return padLine(line, cols) + "\n" + padLine("", cols)
		}
	case rightSFTP:
		title = a.sftpName
		detail = a.sftpAddr
		if a.connectingSFTP != "" {
			detail = "connecting…"
		}
	case rightError:
		title = "Connection failed"
		detail = a.lastAttempt.Title()
	case rightImport:
		title = "Import ssh config"
		if a.importing != nil && len(a.importing.rows) == 0 {
			detail = "reading…"
		}
	case rightSync:
		title = "Sync"
		detail = "private GitHub repo"
	}

	line := strings.Repeat(" ", padX) + styleTitleBar.Render(title)
	if detail != "" {
		line += styleTitleDetail.Render("  " + detail)
	}
	return padLine(line, cols) + "\n" + padLine("", cols)
}

func (a *App) rightBody(cols, rows int) string {
	// A key passphrase question is the newest thing on screen, so it owns the
	// body the way a confirmation does.
	if a.keyPass != nil {
		return inset(a.keyPassView())
	}
	// A confirmation replaces the body outright — see confirm.go for why it is
	// not drawn as an overlay.
	if a.confirm != nil {
		return inset(a.confirm.View())
	}
	switch a.rightMode {
	case rightForm:
		return inset(a.form.View())

	case rightTerminal:
		t := a.cur()
		if t == nil {
			return inset("no session")
		}
		if t.state == tabConnecting {
			return inset(fmt.Sprintf("connecting to %s…", t.name))
		}
		// A lost session keeps its last screen on purpose: it says what you were
		// in the middle of, and the reconnect will wipe it soon enough.
		live := t.state == tabLive
		return renderSelected(t.emu(), cols, rows, t.scrollOff, t.sel,
			live && a.focus == focusSession && t.scrollOff == 0)

	case rightError:
		return inset(errorCard(a.failErr, a.lastAttempt))

	case rightImport:
		if a.importing == nil {
			return inset("no ssh config")
		}
		if len(a.importing.rows) == 0 {
			return inset("reading " + a.importing.path + "…")
		}
		return inset(a.importing.View(cols-2*padX, rows))

	case rightSync:
		if a.syncForm == nil {
			return inset("sync")
		}
		return inset(a.syncForm.View())

	default:
		var b strings.Builder
		b.WriteString("Pick a server on the left to open a session,\n")
		b.WriteString("press f to browse its files,\n")
		b.WriteString("or choose “+ Connect” to register a new one.\n\n")
		b.WriteString(styleHint.Render("↑/↓ move · enter session · f files · e edit · d delete · q quit"))
		if a.errMsg != "" {
			b.WriteString("\n\n")
			b.WriteString(styleError.Render("✗ " + a.errMsg))
		}
		return inset(b.String())
	}
}

// inset applies the panel's horizontal padding. The title bar already supplies
// the vertical spacing, and clampBlock trims the result back to the panel width,
// so the padding never widens the layout.
func inset(s string) string {
	return lipgloss.NewStyle().PaddingLeft(padX).Render(s)
}

// statusLine composes the bar as [message … help cell].
//
// Until v7 this was a single switch, so a warning, a transfer or an error took
// the whole line and the shortcut hints vanished with it — which is how the one
// key that shows all the others disappeared exactly when it was most wanted.
// The message keeps that switch; the help cell is pinned to the right edge and
// is dropped last, after the message has been truncated to make room for it.
func (a *App) statusLine() string {
	msg := a.statusMessage()
	cell := a.helpCell()

	// Without a width there is nothing to arrange against: tests and the first
	// frame both land here.
	if a.width <= 0 {
		return msg
	}
	if cell == "" {
		// Still trimmed here rather than left to padLine, so the cut is ours
		// and ends in an ellipsis instead of mid-word.
		if ansi.StringWidth(msg) > a.width {
			msg = ansi.Truncate(msg, a.width, "…")
		}
		return msg
	}
	cellW := ansi.StringWidth(cell)
	if a.width < cellW+statusMinMessage {
		// Too narrow for both. A line with no message at all is worse than one
		// with no hint, so this is the single case where the cell gives way.
		return msg
	}

	room := a.width - cellW - 1
	if ansi.StringWidth(msg) > room {
		msg = ansi.Truncate(msg, room, "…")
	}
	gap := maxInt(a.width-ansi.StringWidth(msg)-cellW, 1)
	return msg + strings.Repeat(" ", gap) + cell
}

// statusMinMessage is how much room the message must keep for the help cell to
// be worth pinning.
const statusMinMessage = 12

// statusRoom is how wide the message half of the status bar may be. Anything
// that sizes itself to the bar — the hint line, the progress bar — asks here
// rather than using a.width, or it would be built to overflow and then be cut.
func (a *App) statusRoom() int {
	if a.width <= 0 {
		return 0
	}
	cell := ansi.StringWidth(a.helpCell())
	if cell == 0 || a.width < cell+statusMinMessage {
		return a.width
	}
	return a.width - cell - 1
}

// helpCell is the right-hand end of the status bar. It advertises the help key
// only where that key would actually work: inside a session it belongs to the
// shell, and over a dialog nothing may be opened at all. A key on screen that
// does nothing when pressed is worse than no hint.
func (a *App) helpCell() string {
	key := a.keys.Key(ctxGlobal, actHelp)
	if key == "" {
		return ""
	}
	switch {
	case a.help != nil:
		return styleHint.Render("esc close · / search")
	case a.modalUp():
		return "" // the card on screen already shows its own answers
	case a.focus == focusSession:
		// ? is the shell's here, so the cell says how to get to it.
		return styleHint.Render(a.keys.Key(ctxSession, actEscape) + " " + key + " help")
	case a.focus == focusForm:
		return "" // every key is going into a text field
	case a.focus == focusSidebar && a.sidebar.Filtering():
		return ""
	case !a.helpFits():
		return "" // no room to float the card, so no reason to name the key
	}
	return styleHint.Render(key + " help")
}

func (a *App) statusMessage() string {
	switch {
	case a.transfer != nil:
		return a.transferStatus()
	case a.drag != nil:
		return styleWarning.Render("↦ " + a.drag.label() + " · drop on the other pane to transfer")
	case a.scanning:
		return styleStatus.Render("scanning…")
	case a.warning != "":
		return styleWarning.Render(a.warning)
	case a.errMsg != "" && a.rightMode != rightEmpty:
		return styleError.Render("✗ " + a.errMsg)
	// A session that is down owns the status line: the countdown and the way
	// out are the only things worth saying while it is.
	case a.rightMode == rightTerminal && a.cur() != nil && a.cur().state != tabLive:
		return a.tabStatus(a.cur())
	case a.status != "":
		return styleStatus.Render(a.status)
	case a.sidebar.Filtering():
		// The filter is the list's own input, not a set of bindings, so this one
		// line stays written out.
		return styleStatus.Render("type to filter · enter keep results · esc clear")
	case a.rightMode == rightImport:
		return a.hintFor(ctxImport)
	case a.rightMode == rightSync:
		return a.hintFor(ctxSync)
	case a.rightMode == rightSFTP:
		return a.hintFor(ctxSFTP)
	case len(a.tabs) > 1:
		return a.hintFor(ctxTabs, ctxSidebar, ctxSession, ctxGlobal)
	default:
		return a.hintFor(ctxSidebar, ctxSession, ctxGlobal)
	}
}

// hintFor builds the shortcut line from the bindings themselves, so it cannot
// drift from what the keys actually do. Bindings appear in declaration order,
// and when the line does not fit the lowest-priority one goes first — the help
// cell statusLine pins is not part of this budget and never competes with it.
func (a *App) hintFor(ctxs ...Context) string {
	type item struct {
		text     string
		priority int
	}
	var items []item
	for _, ctx := range ctxs {
		for _, b := range a.keys.Bindings(ctx) {
			if b.Priority <= 0 || b.Short == "" {
				continue
			}
			items = append(items, item{text: b.KeyList() + " " + b.Short, priority: b.Priority})
		}
	}
	if len(items) == 0 {
		return ""
	}

	// Room for the hint is what the help cell leaves. Working it out here is
	// what keeps lipgloss from cutting a sentence in half at the frame edge.
	room := a.statusRoom()
	if a.width <= 0 {
		room = 1 << 30
	}

	join := func(items []item) string {
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i] = it.text
		}
		return strings.Join(parts, " · ")
	}

	for len(items) > 1 && ansi.StringWidth(join(items)) > room {
		// Drop the least important, scanning from the right so that ties lose
		// the one furthest from the front of the line.
		worst := 0
		for i, it := range items {
			if it.priority <= items[worst].priority {
				worst = i
			}
		}
		items = append(items[:worst], items[worst+1:]...)
	}
	return styleStatus.Render(join(items))
}
