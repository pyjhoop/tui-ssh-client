package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/model"
)

// TestDefaultsMatchV6 pins every key the app answered to before the registry
// existed. v7 moved where the bindings are written down; it did not change one
// of them, and this table is what says so.
func TestDefaultsMatchV6(t *testing.T) {
	want := map[Context]map[string]Action{
		ctxGlobal: {
			"?": actHelp, "q": actQuit, "ctrl+c": actQuit,
		},
		ctxSidebar: {
			"enter": actConnect, "n": actNewSession, "e": actEditServer, "d": actDeleteEntry,
			"f": actOpenFiles, "tab": actFocusPanel, " ": actToggleGroup,
			"left": actFoldGroup, "right": actUnfoldGroup, "s": actSortRecent,
			"i": actImport, "Y": actSyncSetup, "S": actSyncPush, "P": actSyncPull,
		},
		ctxSession: {
			"ctrl+b": actEscape, "r": actReconnect,
			"shift+up": actScrollUp, "shift+down": actScrollDown,
			"shift+pgup": actScrollPageUp, "shift+pgdown": actScrollPageDown,
		},
		ctxTabs: {
			"alt+left": actTabPrev, "alt+h": actTabPrev,
			"alt+right": actTabNext, "alt+l": actTabNext,
			"alt+w": actTabClose,
			"alt+1": actTabSelect, "alt+5": actTabSelect, "alt+9": actTabSelect,
		},
		ctxSFTP: {
			"ctrl+b": actSFTPBack, "esc": actSFTPBack,
			"tab": actSFTPSwitchPane, "left": actSFTPSwitchPane, "right": actSFTPSwitchPane,
			"h": actSFTPSwitchPane, "l": actSFTPSwitchPane,
			" ": actSFTPMark, "a": actSFTPClearMarks, "t": actSFTPTransfer,
			"d": actSFTPDelete, "R": actSFTPRename, "r": actSFTPRefresh,
			"up": actSFTPUp, "k": actSFTPUp, "down": actSFTPDown, "j": actSFTPDown,
			"pgup": actSFTPPageUp, "pgdown": actSFTPPageDown,
			"home": actSFTPHome, "end": actSFTPEnd,
			"backspace": actSFTPParent, "enter": actSFTPOpen,
		},
		ctxImport: {
			"esc": actImportCancel, "q": actImportCancel, "ctrl+c": actImportCancel,
			"up": actImportUp, "k": actImportUp, "down": actImportDown, "j": actImportDown,
			" ": actImportToggle, "a": actImportToggleAll, "enter": actImportAccept,
		},
		ctxSync: {
			"esc": actSyncCancel, "tab": actSyncNext, "down": actSyncNext,
			"shift+tab": actSyncPrev, "up": actSyncPrev, "enter": actSyncSubmit,
		},
		ctxConfirm: {
			"enter": actConfirmYes, "y": actConfirmYes, "Y": actConfirmYes,
			"esc": actConfirmNo, "n": actConfirmNo, "N": actConfirmNo,
			"q": actConfirmNo, "ctrl+c": actConfirmNo,
		},
		ctxError: {
			"r": actErrorRetry, "e": actErrorEdit, "esc": actErrorDismiss,
		},
		ctxUnlock: {
			"ctrl+c": actUnlockQuit, "esc": actUnlockCancel, "enter": actUnlockSubmit,
			"tab": actUnlockField, "shift+tab": actUnlockField, "up": actUnlockField, "down": actUnlockField,
		},
	}

	km := DefaultKeymap()
	for ctx, keys := range want {
		for key, act := range keys {
			if got := km.Action(ctx, key); got != act {
				t.Errorf("%s: key %q resolves to %q, want %q", ctx, key, got, act)
			}
		}
	}

	// The sidebar's q must not have become a sidebar binding: quitting is global
	// and looked up on purpose, which is why q does nothing in the file panes.
	if got := km.Action(ctxSFTP, "q"); got != "" {
		t.Errorf("q is bound in the file panes (%q) — it was not in v6", got)
	}
	if got := km.Action(ctxSidebar, "?"); got != "" {
		t.Errorf("? must stay a global binding, got %q in the sidebar", got)
	}
}

func TestNoDuplicateKeyInContext(t *testing.T) {
	km := DefaultKeymap()
	for _, ctx := range contextOrder {
		seen := map[string]Action{}
		for _, b := range km.Bindings(ctx) {
			if b.Doc {
				continue
			}
			for _, key := range b.Keys {
				if other, ok := seen[key]; ok {
					t.Errorf("%s: %q is bound to both %s and %s", ctx, key, other, b.Action)
				}
				seen[key] = b.Action
			}
		}
	}
}

// TestEveryActionIsDispatched guards the one way this design can rot: an action
// that is declared, drawn in the help card and then handled by nobody. The help
// card may not advertise a key that does nothing.
func TestEveryActionIsDispatched(t *testing.T) {
	sources := map[string]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "keymap.go" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = string(b)
	}

	for _, b := range defaultBindings {
		if b.Doc {
			continue // dispatched by the component that owns the input
		}
		found := false
		for _, src := range sources {
			// keymap.go is excluded above, so a mention anywhere else is a
			// handler: these identifiers exist for nothing but dispatch.
			if strings.Contains(src, actionConst(b.Action)) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s is declared but never dispatched", b.Action)
		}
	}
}

// actionConst maps an action id back to its Go identifier, so the source scan
// above looks for what the handlers actually write.
func actionConst(act Action) string {
	for name, id := range actionNames {
		if id == act {
			return name
		}
	}
	return string(act)
}

var actionNames = map[string]Action{
	"actHelp": actHelp, "actQuit": actQuit,
	"actConnect": actConnect, "actNewSession": actNewSession, "actEditServer": actEditServer,
	"actDeleteEntry": actDeleteEntry, "actOpenFiles": actOpenFiles, "actFocusPanel": actFocusPanel,
	"actFilter": actFilter, "actMoveCursor": actMoveCursor, "actToggleGroup": actToggleGroup,
	"actFoldGroup": actFoldGroup, "actUnfoldGroup": actUnfoldGroup, "actSortRecent": actSortRecent,
	"actImport": actImport, "actSyncSetup": actSyncSetup, "actSyncPush": actSyncPush,
	"actSyncPull": actSyncPull, "actEscape": actEscape, "actScrollUp": actScrollUp,
	"actScrollDown": actScrollDown, "actScrollPageUp": actScrollPageUp,
	"actScrollPageDown": actScrollPageDown, "actReconnect": actReconnect,
	"actTabSelect": actTabSelect, "actTabPrev": actTabPrev, "actTabNext": actTabNext,
	"actTabClose": actTabClose, "actSFTPSwitchPane": actSFTPSwitchPane, "actSFTPMark": actSFTPMark,
	"actSFTPClearMarks": actSFTPClearMarks, "actSFTPTransfer": actSFTPTransfer,
	"actSFTPDelete": actSFTPDelete, "actSFTPRename": actSFTPRename, "actSFTPRefresh": actSFTPRefresh,
	"actSFTPOpen": actSFTPOpen, "actSFTPParent": actSFTPParent, "actSFTPUp": actSFTPUp,
	"actSFTPDown": actSFTPDown, "actSFTPPageUp": actSFTPPageUp, "actSFTPPageDown": actSFTPPageDown,
	"actSFTPHome": actSFTPHome, "actSFTPEnd": actSFTPEnd, "actSFTPDrag": actSFTPDrag,
	"actSFTPBack": actSFTPBack, "actImportToggle": actImportToggle,
	"actImportToggleAll": actImportToggleAll, "actImportAccept": actImportAccept,
	"actImportCancel": actImportCancel, "actImportUp": actImportUp, "actImportDown": actImportDown,
	"actSyncNext": actSyncNext, "actSyncPrev": actSyncPrev, "actSyncSubmit": actSyncSubmit,
	"actSyncCancel": actSyncCancel, "actConfirmYes": actConfirmYes, "actConfirmNo": actConfirmNo,
	"actErrorRetry": actErrorRetry, "actErrorEdit": actErrorEdit, "actErrorDismiss": actErrorDismiss,
	"actUnlockSubmit": actUnlockSubmit, "actUnlockField": actUnlockField,
	"actUnlockCancel": actUnlockCancel, "actUnlockQuit": actUnlockQuit,
	"actHelpClose": actHelpClose, "actHelpSearch": actHelpSearch, "actHelpUp": actHelpUp,
	"actHelpDown": actHelpDown, "actHelpPageUp": actHelpPageUp, "actHelpPgDown": actHelpPgDown,
}

// ── user rebinding ──────────────────────────────────────────────────────────

func TestUserRebindApplies(t *testing.T) {
	km := DefaultKeymap()
	problems := km.Apply(map[string][]string{"sidebar.delete": {"ctrl+d"}})
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if got := km.Action(ctxSidebar, "ctrl+d"); got != actDeleteEntry {
		t.Errorf("ctrl+d resolves to %q, want the delete action", got)
	}
	if got := km.Action(ctxSidebar, "d"); got != "" {
		t.Errorf("d is still bound to %q after being rebound away", got)
	}
}

// TestRebindingReachesTheApp is the end of that path: the app itself must obey
// keys.json, not just the table.
func TestRebindingReachesTheApp(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "keys.json"), `{"sidebar.delete": ["ctrl+d"]}`)

	app := New(config.New(dir))
	app.applyKeymap(keymapLoadedMsg{keys: mustLoadKeys(t, app)})
	app.resize(100, 30)
	app.servers = []model.Server{{ID: "1", Name: "alpha", Host: "a", User: "u", Port: 22}}
	app.sidebar.SetServers(app.servers)
	app.sidebar.list.Select(1) // past "+ Connect"

	app.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	if app.confirm != nil {
		t.Fatal("d still deletes after it was rebound away")
	}
	app.handleKey(tea.KeyMsg{Type: tea.KeyCtrlD})
	if app.confirm == nil {
		t.Fatal("ctrl+d did not ask to delete")
	}
}

func TestUnknownActionIsReported(t *testing.T) {
	km := DefaultKeymap()
	problems := km.Apply(map[string][]string{"sidebar.explode": {"x"}})
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "no such action") {
		t.Fatalf("want one unknown-action problem, got %v", problems)
	}
}

func TestDocBindingsAreNotRebindable(t *testing.T) {
	km := DefaultKeymap()
	problems := km.Apply(map[string][]string{"form.save": {"ctrl+x"}})
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "not rebindable") {
		t.Fatalf("want a not-rebindable problem, got %v", problems)
	}
}

// TestDuplicateRebindKeepsDefaults: a key that would mean two things in one
// context sends both actions back to their defaults. Letting the first entry
// win would make the order of a JSON object load-bearing.
func TestDuplicateRebindKeepsDefaults(t *testing.T) {
	km := DefaultKeymap()
	problems := km.Apply(map[string][]string{"sidebar.delete": {"e"}})

	if len(problems) == 0 {
		t.Fatal("a clash was accepted silently")
	}
	if got := km.Action(ctxSidebar, "d"); got != actDeleteEntry {
		t.Errorf("delete did not go back to d, got %q", got)
	}
	if got := km.Action(ctxSidebar, "e"); got != actEditServer {
		t.Errorf("e no longer edits, got %q", got)
	}
}

// TestEscapeCannotBeUnbound and its sibling below: two bindings may never be
// left without a key, because losing them locks the user out of the session and
// out of the documentation respectively.
func TestEscapeCannotBeUnbound(t *testing.T) {
	km := DefaultKeymap()
	problems := km.Apply(map[string][]string{"session.escape": {}})
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %v", problems)
	}
	if got := km.Action(ctxSession, escapeHint); got != actEscape {
		t.Errorf("the escape key was lost: %q resolves to %q", escapeHint, got)
	}
}

func TestHelpCannotBeUnbound(t *testing.T) {
	km := DefaultKeymap()
	km.Apply(map[string][]string{"global.help": {}})
	if got := km.Action(ctxGlobal, "?"); got != actHelp {
		t.Errorf("the help key was lost: ? resolves to %q", got)
	}
}

// TestKeymapProblemsSurfaceOnce: a broken file is reported, not swallowed —
// this is the whole difference from ui.json.
func TestBrokenKeysFileIsReported(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "keys.json"), `{not json`)

	app := New(config.New(dir))
	_, err := app.store.LoadKeys()
	if err == nil {
		t.Fatal("a corrupt keys.json must be an error")
	}
	app.applyKeymap(keymapLoadedMsg{err: err})
	if len(app.keyProblems) == 0 {
		t.Fatal("nothing was reported")
	}
	if !strings.Contains(app.status, "keys.json") {
		t.Errorf("status line says nothing about it: %q", app.status)
	}
	// The defaults still work: one bad file may not disarm the keyboard.
	if got := app.keys.Action(ctxSidebar, "d"); got != actDeleteEntry {
		t.Errorf("defaults were lost, d resolves to %q", got)
	}
}

// TestTabSelectUsesKeyPosition: which tab alt+N switches to comes from the
// position of the key in the binding, so rebinding the family keeps working.
func TestTabSelectUsesKeyPosition(t *testing.T) {
	km := DefaultKeymap()
	if got := km.KeyIndex(ctxTabs, actTabSelect, "alt+3"); got != 2 {
		t.Errorf("alt+3 is index %d, want 2", got)
	}
	km.Apply(map[string][]string{"tabs.select": {"f1", "f2", "f3"}})
	if got := km.KeyIndex(ctxTabs, actTabSelect, "f2"); got != 1 {
		t.Errorf("f2 is index %d, want 1", got)
	}
}

func TestDumpJSONRoundTrips(t *testing.T) {
	dir := t.TempDir()
	store := config.New(dir)
	km := DefaultKeymap()
	write(t, store.KeysPath(), km.DumpJSON())

	out, problems, err := KeyDump(store, false)
	if err != nil {
		t.Fatalf("KeyDump: %v", err)
	}
	if len(problems) != 0 {
		t.Errorf("the dumped defaults were not accepted back: %v", problems)
	}
	if !strings.Contains(out, "sidebar.delete") {
		t.Error("the text dump does not mention the actions")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustLoadKeys(t *testing.T, app *App) map[string][]string {
	t.Helper()
	keys, err := app.store.LoadKeys()
	if err != nil {
		t.Fatal(err)
	}
	return keys
}
