package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pyjhoop/tui-ssh-client/internal/config"
)

// Context is where a key means something. Exactly one context is active when a
// key arrives, and which one it is stays the business of the handlers: the
// modal rules that decide it are the same ones as before this file existed.
type Context string

const (
	ctxGlobal  Context = "global"  // help and quit — looked up explicitly, never as a fallback
	ctxSidebar Context = "sidebar" // the server list, "+ Connect" and group headers
	ctxSession Context = "session" // the escape key and the scrollback; everything else is the shell's
	ctxTabs    Context = "tabs"    // alt+… : works from anywhere a session is open
	ctxSFTP    Context = "sftp"
	ctxForm    Context = "form"
	ctxImport  Context = "import"
	ctxSync    Context = "sync"
	ctxConfirm Context = "confirm" // yes/no panels: host key, delete, transfer, pull
	ctxError   Context = "error"   // the connection error card
	ctxUnlock  Context = "unlock"
	ctxPrompt  Context = "prompt" // one-line questions: rename, key passphrase
	ctxHelp    Context = "help"
)

// contextOrder is the order sections appear in the help card. The context the
// card was opened from is lifted to the top; the rest keep this order so the
// card does not reshuffle itself between openings.
var contextOrder = []Context{
	ctxGlobal, ctxSidebar, ctxSession, ctxTabs, ctxSFTP,
	ctxForm, ctxImport, ctxSync, ctxConfirm, ctxError, ctxUnlock, ctxPrompt, ctxHelp,
}

var contextTitles = map[Context]string{
	ctxGlobal:  "Anywhere",
	ctxSidebar: "Server list",
	ctxSession: "Terminal session",
	ctxTabs:    "Tabs",
	ctxSFTP:    "Files (SFTP)",
	ctxForm:    "Connection form",
	ctxImport:  "Import ssh config",
	ctxSync:    "Sync",
	ctxConfirm: "Confirmation",
	ctxError:   "Connection error",
	ctxUnlock:  "Vault",
	ctxPrompt:  "One-line prompts",
	ctxHelp:    "This card",
}

// Action is what a key does. The id is the name the user writes in keys.json and
// the label the tests assert on, so it is stable once shipped: renaming one
// silently breaks somebody's file.
type Action string

const (
	actHelp Action = "global.help"
	actQuit Action = "global.quit"

	actConnect     Action = "sidebar.connect"
	actNewSession  Action = "sidebar.new_session"
	actEditServer  Action = "sidebar.edit"
	actDeleteEntry Action = "sidebar.delete"
	actOpenFiles   Action = "sidebar.files"
	actFocusPanel  Action = "sidebar.focus_panel"
	actFilter      Action = "sidebar.filter"
	actMoveCursor  Action = "sidebar.move"
	actToggleGroup Action = "sidebar.toggle_group"
	actFoldGroup   Action = "sidebar.fold_group"
	actUnfoldGroup Action = "sidebar.unfold_group"
	actSortRecent  Action = "sidebar.sort_recent"
	actImport      Action = "sidebar.import"
	actSyncSetup   Action = "sidebar.sync_setup"
	actSyncPush    Action = "sidebar.sync_push"
	actSyncPull    Action = "sidebar.sync_pull"

	actEscape         Action = "session.escape"
	actScrollUp       Action = "session.scroll_up"
	actScrollDown     Action = "session.scroll_down"
	actScrollPageUp   Action = "session.scroll_page_up"
	actScrollPageDown Action = "session.scroll_page_down"
	actReconnect      Action = "session.reconnect"
	actCopy           Action = "session.copy"

	actTabSelect Action = "tabs.select"
	actTabPrev   Action = "tabs.prev"
	actTabNext   Action = "tabs.next"
	actTabClose  Action = "tabs.close"

	actSFTPSwitchPane Action = "sftp.switch_pane"
	actSFTPMark       Action = "sftp.mark"
	actSFTPClearMarks Action = "sftp.clear_marks"
	actSFTPTransfer   Action = "sftp.transfer"
	actSFTPDelete     Action = "sftp.delete"
	actSFTPRename     Action = "sftp.rename"
	actSFTPRefresh    Action = "sftp.refresh"
	actSFTPOpen       Action = "sftp.open"
	actSFTPParent     Action = "sftp.parent"
	actSFTPUp         Action = "sftp.up"
	actSFTPDown       Action = "sftp.down"
	actSFTPPageUp     Action = "sftp.page_up"
	actSFTPPageDown   Action = "sftp.page_down"
	actSFTPHome       Action = "sftp.home"
	actSFTPEnd        Action = "sftp.end"
	actSFTPDrag       Action = "sftp.drag"
	actSFTPBack       Action = "sftp.back"

	actFormCancel Action = "form.cancel"
	actFormMove   Action = "form.move"
	actFormAuth   Action = "form.auth"
	actFormSave   Action = "form.save"

	actImportToggle    Action = "import.toggle"
	actImportToggleAll Action = "import.toggle_all"
	actImportAccept    Action = "import.accept"
	actImportCancel    Action = "import.cancel"
	actImportUp        Action = "import.up"
	actImportDown      Action = "import.down"

	actSyncNext   Action = "sync.next_field"
	actSyncPrev   Action = "sync.prev_field"
	actSyncSubmit Action = "sync.submit"
	actSyncCancel Action = "sync.cancel"

	actConfirmYes Action = "confirm.yes"
	actConfirmNo  Action = "confirm.no"

	actErrorRetry   Action = "error.retry"
	actErrorEdit    Action = "error.edit"
	actErrorDismiss Action = "error.dismiss"

	actUnlockSubmit Action = "unlock.submit"
	actUnlockField  Action = "unlock.switch_field"
	actUnlockCancel Action = "unlock.cancel"
	actUnlockQuit   Action = "unlock.quit"

	actPromptAccept Action = "prompt.accept"
	actPromptCancel Action = "prompt.cancel"

	actHelpClose  Action = "help.close"
	actHelpSearch Action = "help.search"
	actHelpUp     Action = "help.scroll_up"
	actHelpDown   Action = "help.scroll_down"
	actHelpPageUp Action = "help.page_up"
	actHelpPgDown Action = "help.page_down"
)

// Binding is one row of the help card and one case of a switch.
type Binding struct {
	Action Action
	Ctx    Context
	// Keys are tea.KeyMsg.String() values. The first one is what the status
	// line shows unless Label overrides the whole list.
	Keys []string
	// Label replaces the rendered key list, for families that read better
	// collapsed ("alt+1..9") than enumerated.
	Label string
	// Desc is the help card's sentence; Short is the status line's word.
	Desc  string
	Short string
	// Priority decides what survives when the status line runs out of room:
	// 0 keeps a binding off the line entirely, and the lowest priority left is
	// the next one dropped. It has nothing to do with dispatch.
	Priority int
	// Doc marks a binding that is described here but dispatched elsewhere: the
	// connection form reads msg.Type against the focused field, the list owns
	// its own filter and cursor, and a drag is not a key at all. They are
	// listed because the user still has to learn them, and they are not
	// rebindable, because nothing here would honour the change.
	Doc bool
}

// KeyList is the binding's keys as the help card shows them.
func (b Binding) KeyList() string {
	if b.Label != "" {
		return b.Label
	}
	if len(b.Keys) == 0 {
		return "—"
	}
	keys := make([]string, len(b.Keys))
	for i, k := range b.Keys {
		keys[i] = prettyKey(k)
	}
	return strings.Join(keys, ", ")
}

// prettyKey makes a Bubble Tea key name readable. Only the ones that look like
// nothing on screen are translated.
func prettyKey(k string) string {
	switch k {
	case " ":
		return "space"
	case "up":
		return "↑"
	case "down":
		return "↓"
	case "left":
		return "←"
	case "right":
		return "→"
	}
	return k
}

// defaultBindings is the whole keymap. Declaration order is display order, both
// in the help card and on the status line.
//
// Every key here was taken from the handlers as they stood in v6: this table
// changed where the keys are written down, not what they do.
var defaultBindings = []Binding{
	// ── global ──
	{Action: actHelp, Ctx: ctxGlobal, Keys: []string{"?"}, Desc: "show this help", Short: "help"},
	{Action: actQuit, Ctx: ctxGlobal, Keys: []string{"q", "ctrl+c"}, Label: "q", Desc: "quit ssh-client", Short: "quit", Priority: 10},

	// ── sidebar ──
	{Action: actFocusPanel, Ctx: ctxSidebar, Keys: []string{"tab"}, Desc: "move focus into the right panel", Short: "focus panel", Priority: 50},
	{Action: actNewSession, Ctx: ctxSidebar, Keys: []string{"n"}, Desc: "open a second session to this server", Short: "new session", Priority: 45},
	{Action: actOpenFiles, Ctx: ctxSidebar, Keys: []string{"f"}, Desc: "browse this server's files (SFTP)", Short: "files", Priority: 40},
	{Action: actConnect, Ctx: ctxSidebar, Keys: []string{"enter"}, Desc: "open a session, or fold a group header"},
	{Action: actMoveCursor, Ctx: ctxSidebar, Keys: []string{"up", "down"}, Label: "↑/↓", Desc: "move the cursor", Doc: true},
	{Action: actEditServer, Ctx: ctxSidebar, Keys: []string{"e"}, Desc: "edit the highlighted server"},
	{Action: actDeleteEntry, Ctx: ctxSidebar, Keys: []string{"d"}, Desc: "delete the highlighted server (asks first)"},
	{Action: actFilter, Ctx: ctxSidebar, Keys: []string{"/"}, Desc: "filter the list — esc clears it", Doc: true},
	{Action: actToggleGroup, Ctx: ctxSidebar, Keys: []string{" "}, Desc: "fold or unfold the group under the cursor"},
	{Action: actFoldGroup, Ctx: ctxSidebar, Keys: []string{"left"}, Desc: "fold the group"},
	{Action: actUnfoldGroup, Ctx: ctxSidebar, Keys: []string{"right"}, Desc: "unfold the group"},
	{Action: actSortRecent, Ctx: ctxSidebar, Keys: []string{"s"}, Desc: "sort by last used, or back to saved order"},
	{Action: actImport, Ctx: ctxSidebar, Keys: []string{"i"}, Desc: "import ~/.ssh/config"},
	{Action: actSyncSetup, Ctx: ctxSidebar, Keys: []string{"Y"}, Desc: "set up sync with a private GitHub repo"},
	{Action: actSyncPush, Ctx: ctxSidebar, Keys: []string{"S"}, Desc: "push — local to remote"},
	{Action: actSyncPull, Ctx: ctxSidebar, Keys: []string{"P"}, Desc: "pull — remote to local (asks first)"},

	// ── session ──
	{Action: actEscape, Ctx: ctxSession, Keys: []string{escapeHint}, Desc: "leave the session — it keeps running", Short: "leave session", Priority: 30},
	{Action: actScrollUp, Ctx: ctxSession, Keys: []string{"shift+up"}, Desc: "scroll back"},
	{Action: actScrollDown, Ctx: ctxSession, Keys: []string{"shift+down"}, Desc: "scroll forward"},
	{Action: actScrollPageUp, Ctx: ctxSession, Keys: []string{"shift+pgup"}, Desc: "scroll back a page"},
	{Action: actScrollPageDown, Ctx: ctxSession, Keys: []string{"shift+pgdown"}, Desc: "scroll forward a page"},
	{Action: actReconnect, Ctx: ctxSession, Keys: []string{"r"}, Desc: "reconnect now, without waiting out the backoff"},
	// Priority 0 keeps this off the status line: the line that lists session
	// bindings is the one drawn beside the sidebar, and a gesture that only works
	// inside a session must not be advertised where it would do nothing.
	{Action: actCopy, Ctx: ctxSession, Keys: nil, Label: "drag", Desc: "select and copy to the clipboard (OSC 52)", Doc: true},

	// ── tabs ──
	{Action: actTabSelect, Ctx: ctxTabs, Keys: []string{"alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9"}, Label: "alt+1..9", Desc: "switch to the nth session", Short: "tab", Priority: 25},
	{Action: actTabNext, Ctx: ctxTabs, Keys: []string{"alt+right", "alt+l"}, Desc: "next session", Short: "next", Priority: 24},
	{Action: actTabPrev, Ctx: ctxTabs, Keys: []string{"alt+left", "alt+h"}, Desc: "previous session"},
	{Action: actTabClose, Ctx: ctxTabs, Keys: []string{"alt+w"}, Desc: "close the current session", Short: "close", Priority: 23},

	// ── sftp ──
	{Action: actSFTPSwitchPane, Ctx: ctxSFTP, Keys: []string{"tab", "left", "right", "h", "l"}, Label: "tab", Desc: "switch panes", Short: "pane", Priority: 50},
	{Action: actSFTPMark, Ctx: ctxSFTP, Keys: []string{" "}, Desc: "select or deselect the row", Short: "select", Priority: 45},
	{Action: actSFTPTransfer, Ctx: ctxSFTP, Keys: []string{"t"}, Desc: "send the selection, or the row under the cursor", Short: "send", Priority: 40},
	{Action: actSFTPDelete, Ctx: ctxSFTP, Keys: []string{"d"}, Desc: "delete (asks first)", Short: "delete", Priority: 32},
	{Action: actSFTPRename, Ctx: ctxSFTP, Keys: []string{"R"}, Desc: "rename", Short: "rename", Priority: 28},
	{Action: actSFTPRefresh, Ctx: ctxSFTP, Keys: []string{"r"}, Desc: "re-read the directory", Short: "refresh", Priority: 26},
	{Action: actSFTPDrag, Ctx: ctxSFTP, Keys: nil, Label: "drag", Desc: "drag rows onto the other pane to transfer", Short: "to transfer", Priority: 22, Doc: true},
	{Action: actSFTPBack, Ctx: ctxSFTP, Keys: []string{escapeHint, "esc"}, Label: escapeHint, Desc: "back to the list — the connection stays up", Short: "back", Priority: 35},
	{Action: actSFTPClearMarks, Ctx: ctxSFTP, Keys: []string{"a"}, Desc: "clear the selection"},
	{Action: actSFTPOpen, Ctx: ctxSFTP, Keys: []string{"enter"}, Desc: "enter the directory, or send the file"},
	{Action: actSFTPParent, Ctx: ctxSFTP, Keys: []string{"backspace"}, Desc: "go up one directory"},
	{Action: actSFTPUp, Ctx: ctxSFTP, Keys: []string{"up", "k"}, Desc: "move up"},
	{Action: actSFTPDown, Ctx: ctxSFTP, Keys: []string{"down", "j"}, Desc: "move down"},
	{Action: actSFTPPageUp, Ctx: ctxSFTP, Keys: []string{"pgup"}, Desc: "move up a page"},
	{Action: actSFTPPageDown, Ctx: ctxSFTP, Keys: []string{"pgdown"}, Desc: "move down a page"},
	{Action: actSFTPHome, Ctx: ctxSFTP, Keys: []string{"home"}, Desc: "first row"},
	{Action: actSFTPEnd, Ctx: ctxSFTP, Keys: []string{"end"}, Desc: "last row"},

	// ── connection form (dispatched by the form itself — see Doc) ──
	{Action: actFormSave, Ctx: ctxForm, Keys: []string{"enter", "ctrl+s"}, Desc: "save (ctrl+s also works inside the key box)", Doc: true},
	{Action: actFormMove, Ctx: ctxForm, Keys: []string{"tab", "shift+tab"}, Desc: "next / previous field", Doc: true},
	{Action: actFormAuth, Ctx: ctxForm, Keys: []string{"left", "right"}, Label: "←/→", Desc: "change the authentication method", Doc: true},
	{Action: actFormCancel, Ctx: ctxForm, Keys: []string{"esc"}, Desc: "cancel — nothing is saved", Doc: true},

	// ── import preview ──
	{Action: actImportToggle, Ctx: ctxImport, Keys: []string{" "}, Desc: "select or deselect the entry", Short: "select", Priority: 40},
	{Action: actImportToggleAll, Ctx: ctxImport, Keys: []string{"a"}, Desc: "select all or none", Short: "all/none", Priority: 35},
	{Action: actImportAccept, Ctx: ctxImport, Keys: []string{"enter"}, Desc: "import the selected entries", Short: "import", Priority: 45},
	{Action: actImportCancel, Ctx: ctxImport, Keys: []string{"esc", "q", "ctrl+c"}, Label: "esc", Desc: "cancel", Short: "cancel", Priority: 30},
	{Action: actImportUp, Ctx: ctxImport, Keys: []string{"up", "k"}, Desc: "move up"},
	{Action: actImportDown, Ctx: ctxImport, Keys: []string{"down", "j"}, Desc: "move down"},

	// ── sync form ──
	{Action: actSyncNext, Ctx: ctxSync, Keys: []string{"tab", "down"}, Label: "tab", Desc: "next field", Short: "move", Priority: 40},
	{Action: actSyncSubmit, Ctx: ctxSync, Keys: []string{"enter"}, Desc: "check the repo is private, then save", Short: "check and save", Priority: 35},
	{Action: actSyncCancel, Ctx: ctxSync, Keys: []string{"esc"}, Desc: "cancel", Short: "cancel", Priority: 30},
	{Action: actSyncPrev, Ctx: ctxSync, Keys: []string{"shift+tab", "up"}, Desc: "previous field"},

	// ── confirmation panels ──
	{Action: actConfirmYes, Ctx: ctxConfirm, Keys: []string{"enter", "y", "Y"}, Desc: "yes"},
	{Action: actConfirmNo, Ctx: ctxConfirm, Keys: []string{"esc", "n", "N", "q", "ctrl+c"}, Desc: "no"},

	// ── error card ──
	{Action: actErrorRetry, Ctx: ctxError, Keys: []string{"r"}, Desc: "try again"},
	{Action: actErrorEdit, Ctx: ctxError, Keys: []string{"e"}, Desc: "edit the connection"},
	{Action: actErrorDismiss, Ctx: ctxError, Keys: []string{"esc"}, Desc: "dismiss"},

	// ── vault ──
	{Action: actUnlockSubmit, Ctx: ctxUnlock, Keys: []string{"enter"}, Desc: "unlock, or create the vault"},
	{Action: actUnlockField, Ctx: ctxUnlock, Keys: []string{"tab", "shift+tab", "up", "down"}, Label: "tab", Desc: "switch between passphrase and confirmation"},
	{Action: actUnlockCancel, Ctx: ctxUnlock, Keys: []string{"esc"}, Desc: "back out — only when something else asked for the vault"},
	{Action: actUnlockQuit, Ctx: ctxUnlock, Keys: []string{"ctrl+c"}, Desc: "quit"},

	// ── one-line prompts (their own input owns the keyboard — see Doc) ──
	{Action: actPromptAccept, Ctx: ctxPrompt, Keys: []string{"enter"}, Desc: "accept (rename, key passphrase)", Doc: true},
	{Action: actPromptCancel, Ctx: ctxPrompt, Keys: []string{"esc"}, Desc: "cancel", Doc: true},

	// ── this card ──
	{Action: actHelpClose, Ctx: ctxHelp, Keys: []string{"esc", "q"}, Label: "esc", Desc: "close (any other key closes it too)", Short: "close", Priority: 20},
	{Action: actHelpSearch, Ctx: ctxHelp, Keys: []string{"/"}, Desc: "search every context", Short: "search", Priority: 15},
	{Action: actHelpUp, Ctx: ctxHelp, Keys: []string{"up"}, Desc: "scroll up"},
	{Action: actHelpDown, Ctx: ctxHelp, Keys: []string{"down"}, Desc: "scroll down"},
	{Action: actHelpPageUp, Ctx: ctxHelp, Keys: []string{"pgup"}, Desc: "scroll up a page"},
	{Action: actHelpPgDown, Ctx: ctxHelp, Keys: []string{"pgdown"}, Desc: "scroll down a page"},
}

// Keymap resolves keys to actions. It is the single source the dispatcher, the
// status line, the help card and keys.json all read: a binding that is not in
// here does not exist, and one that is in here is documented by construction.
type Keymap struct {
	bindings map[Context][]Binding
	index    map[Context]map[string]Action
}

// DefaultKeymap is the shipped keymap.
func DefaultKeymap() *Keymap {
	k := &Keymap{
		bindings: make(map[Context][]Binding, len(contextOrder)),
		index:    make(map[Context]map[string]Action, len(contextOrder)),
	}
	for _, b := range defaultBindings {
		k.bindings[b.Ctx] = append(k.bindings[b.Ctx], b)
	}
	k.reindex()
	return k
}

func (k *Keymap) reindex() {
	k.index = make(map[Context]map[string]Action, len(k.bindings))
	for ctx, list := range k.bindings {
		m := make(map[string]Action, len(list))
		for _, b := range list {
			if b.Doc {
				continue // described here, dispatched elsewhere
			}
			for _, key := range b.Keys {
				m[key] = b.Action
			}
		}
		k.index[ctx] = m
	}
}

// Action resolves a pressed key inside one context. An empty action means the
// key is not bound here, and the caller falls through exactly as its switch's
// default did before. There is deliberately no global fallback: q quits from the
// sidebar and does nothing in the file panes, and that difference is load-bearing.
func (k *Keymap) Action(ctx Context, key string) Action {
	if k == nil {
		return ""
	}
	return k.index[ctx][key]
}

// KeyIndex reports which of an action's keys was pressed, for the families that
// carry a number (alt+1..9). It is -1 when the key is not one of them.
func (k *Keymap) KeyIndex(ctx Context, act Action, key string) int {
	for _, b := range k.bindings[ctx] {
		if b.Action != act {
			continue
		}
		for i, candidate := range b.Keys {
			if candidate == key {
				return i
			}
		}
	}
	return -1
}

// Bindings are the context's rows in declaration order.
func (k *Keymap) Bindings(ctx Context) []Binding {
	if k == nil {
		return nil
	}
	return k.bindings[ctx]
}

// Binding looks one up by action, for the places that need to print a key
// ("ctrl+b ? help") rather than react to one.
func (k *Keymap) Binding(ctx Context, act Action) (Binding, bool) {
	for _, b := range k.bindings[ctx] {
		if b.Action == act {
			return b, true
		}
	}
	return Binding{}, false
}

// Key is the action's primary key, or "" when it has none.
func (k *Keymap) Key(ctx Context, act Action) string {
	if b, ok := k.Binding(ctx, act); ok && len(b.Keys) > 0 {
		return b.Keys[0]
	}
	return ""
}

// ── user rebinding ──────────────────────────────────────────────────────────

// KeymapProblem is one rejected line of keys.json. Unlike ui.json, this file is
// something the user wrote on purpose, so a mistake in it is reported rather
// than swallowed: silently ignoring it is indistinguishable from a bug.
type KeymapProblem struct {
	Action string
	Reason string
}

func (p KeymapProblem) String() string { return p.Action + ": " + p.Reason }

// Apply overlays the user's bindings, returning what it refused and why. A
// rejected entry falls back to its default; the rest of the file still applies,
// because one typo should not throw away a whole keymap.
func (k *Keymap) Apply(user map[string][]string) []KeymapProblem {
	if len(user) == 0 {
		return nil
	}

	// Deterministic order so the reported problems do not shuffle between runs.
	ids := make([]string, 0, len(user))
	for id := range user {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var problems []KeymapProblem
	touched := map[Context]bool{}
	for _, id := range ids {
		act := Action(id)
		ctx, b, ok := k.find(act)
		if !ok {
			problems = append(problems, KeymapProblem{Action: id, Reason: "no such action"})
			continue
		}
		if b.Doc {
			problems = append(problems, KeymapProblem{Action: id, Reason: "not rebindable"})
			continue
		}
		k.set(ctx, act, append([]string(nil), user[id]...))
		touched[ctx] = true
	}

	// A key that now means two things in one context is worse than a keymap
	// that ignored the file: both actions go back to their defaults, because
	// picking a winner would make the order of a JSON object meaningful.
	for ctx := range touched {
		problems = append(problems, k.resolveClashes(ctx, user)...)
	}

	// Two bindings may never be left without a key: without the escape there is
	// no way out of a session, and without help there is no way to find out.
	problems = append(problems, k.requireBound(ctxSession, actEscape)...)
	problems = append(problems, k.requireBound(ctxGlobal, actHelp)...)

	k.reindex()
	sort.Slice(problems, func(i, j int) bool { return problems[i].Action < problems[j].Action })
	return problems
}

func (k *Keymap) find(act Action) (Context, Binding, bool) {
	for _, ctx := range contextOrder {
		for _, b := range k.bindings[ctx] {
			if b.Action == act {
				return ctx, b, true
			}
		}
	}
	return "", Binding{}, false
}

func (k *Keymap) set(ctx Context, act Action, keys []string) {
	for i, b := range k.bindings[ctx] {
		if b.Action == act {
			k.bindings[ctx][i].Keys = keys
			return
		}
	}
}

// defaultKeysFor is the shipped binding, for restoring a rejected one.
func defaultKeysFor(act Action) []string {
	for _, b := range defaultBindings {
		if b.Action == act {
			return append([]string(nil), b.Keys...)
		}
	}
	return nil
}

func (k *Keymap) resolveClashes(ctx Context, user map[string][]string) []KeymapProblem {
	seen := map[string][]Action{}
	for _, b := range k.bindings[ctx] {
		if b.Doc {
			continue
		}
		for _, key := range b.Keys {
			seen[key] = append(seen[key], b.Action)
		}
	}

	var problems []KeymapProblem
	reverted := map[Action]bool{}
	for key, acts := range seen {
		if len(acts) < 2 {
			continue
		}
		for _, act := range acts {
			if reverted[act] {
				continue
			}
			reverted[act] = true
			k.set(ctx, act, defaultKeysFor(act))
			if _, fromUser := user[string(act)]; fromUser {
				problems = append(problems, KeymapProblem{
					Action: string(act),
					Reason: fmt.Sprintf("%q is already used in this context — keeping the default", key),
				})
			}
		}
	}
	return problems
}

func (k *Keymap) requireBound(ctx Context, act Action) []KeymapProblem {
	b, ok := k.Binding(ctx, act)
	if !ok || len(b.Keys) > 0 {
		return nil
	}
	k.set(ctx, act, defaultKeysFor(act))
	return []KeymapProblem{{Action: string(act), Reason: "cannot be left unbound — keeping the default"}}
}

// KeyDump renders the effective keymap — the defaults with keys.json applied —
// for the --keys flag. It reports the same problems the app would show, so the
// dump and the running program can never disagree about what a key does.
func KeyDump(store *config.Store, asJSON bool) (string, []KeymapProblem, error) {
	user, err := store.LoadKeys()
	if err != nil {
		return "", nil, err
	}
	km := DefaultKeymap()
	problems := km.Apply(user)
	if asJSON {
		return km.DumpJSON(), problems, nil
	}
	return km.Dump(), problems, nil
}

// Dump renders the whole keymap as text, for --keys. It is deliberately the
// same data the help card draws, printed rather than floated.
func (k *Keymap) Dump() string {
	var b strings.Builder
	for _, ctx := range contextOrder {
		list := k.Bindings(ctx)
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s\n", contextTitles[ctx])
		for _, bind := range list {
			note := ""
			if bind.Doc {
				note = "   (not rebindable)"
			}
			fmt.Fprintf(&b, "  %-22s %-46s %s%s\n", bind.KeyList(), bind.Desc, bind.Action, note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// DumpJSON prints the keymap in keys.json's own format, so the file can be
// started from the real defaults instead of from the documentation.
func (k *Keymap) DumpJSON() string {
	var rows []string
	for _, ctx := range contextOrder {
		for _, b := range k.Bindings(ctx) {
			if b.Doc {
				continue
			}
			keys := make([]string, len(b.Keys))
			for i, key := range b.Keys {
				keys[i] = fmt.Sprintf("%q", key)
			}
			rows = append(rows, fmt.Sprintf("  %-28s [%s]", fmt.Sprintf("%q:", b.Action), strings.Join(keys, ", ")))
		}
	}
	return "{\n" + strings.Join(rows, ",\n") + "\n}\n"
}
