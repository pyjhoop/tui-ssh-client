// Command ssh-client is a TUI SSH client: a server list on the left, a live
// embedded PTY session on the right.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/pyjhoop/ssh-client/internal/config"
	syncpkg "github.com/pyjhoop/ssh-client/internal/sync"
	"github.com/pyjhoop/ssh-client/internal/ui"
	"github.com/pyjhoop/ssh-client/internal/vault"
)

func main() {
	pull := flag.Bool("pull", false, "fetch the encrypted bundle from the sync repository before starting")
	repo := flag.String("repo", "", "owner/name of the sync repository (only needed the first time on a machine)")
	path := flag.String("path", ui.DefaultBundlePath, "path to the bundle inside the repository")
	flag.Parse()

	store, err := config.Default()
	if err != nil {
		fail(err)
	}

	app := ui.New(store)
	if *pull {
		pass, sec, err := bootstrap(store, *repo, *path)
		if err != nil {
			fail(err)
		}
		app.Unlocked(pass, sec)
	}

	p := tea.NewProgram(
		app,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	if _, err := p.Run(); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ssh-client:", err)
	os.Exit(1)
}

// bootstrap is the new-machine path: pull the bundle, decrypt it and write the
// local configuration, all before the UI exists.
//
// It runs on a plain terminal rather than through the TUI because it has to
// solve a chicken-and-egg problem: there is no vault yet, so there is no token
// in one, so the coordinates and the credential have to come from outside.
func bootstrap(store *config.Store, repoFlag, path string) (string, vault.Secrets, error) {
	sec := vault.Secrets{Version: vault.CurrentVersion}

	pass, err := readPassphrase("Vault passphrase: ")
	if err != nil {
		return "", sec, err
	}
	if pass == "" {
		return "", sec, errors.New("a passphrase is required")
	}

	// An existing vault on this machine already knows where to look and what to
	// authenticate with; a fresh one has to be told.
	if store.HasVault() {
		sec, err = store.LoadSecrets(pass)
		if err != nil {
			return "", sec, err
		}
	}

	auth, err := resolveAuth(sec.GitHub, repoFlag, path)
	if err != nil {
		return "", sec, err
	}

	remote := &syncpkg.Remote{Token: auth.Token}
	target := syncpkg.Repo{Owner: auth.Owner, Name: auth.Repo, Path: auth.Path}
	// Private is checked on the way in as well as on the way out: a bundle
	// sitting in a public repo is already compromised, and we should say so
	// rather than quietly adopting it.
	if err := remote.Check(target); err != nil {
		return "", sec, err
	}

	cipher, sha, err := remote.Get(target)
	if err != nil {
		return "", sec, err
	}
	blob, err := vault.Decrypt(cipher, pass)
	if err != nil {
		return "", sec, err
	}

	rep, err := store.ApplyBundle(blob, &sec)
	if err != nil {
		return "", sec, err
	}
	auth.SHA = sha
	sec.GitHub = &auth
	if err := store.SaveSecrets(sec, pass); err != nil {
		return "", sec, err
	}

	fmt.Fprintf(os.Stderr, "ssh-client: pulled %d servers, %d host keys added\n",
		rep.Servers, rep.KnownHostsNew)
	return pass, sec, nil
}

// resolveAuth works out where to pull from and what to authenticate with. The
// token comes from the vault, then GITHUB_TOKEN, then `gh auth token` — the
// first machine has no vault, so the last two are the only way in.
func resolveAuth(current *vault.GitHubAuth, repoFlag, path string) (vault.GitHubAuth, error) {
	auth := vault.GitHubAuth{Path: path}
	if current != nil {
		auth = *current
		if path != "" && path != ui.DefaultBundlePath {
			auth.Path = path
		}
	}

	if repoFlag != "" {
		owner, name, err := syncpkg.ParseRepo(repoFlag)
		if err != nil {
			return auth, err
		}
		auth.Owner, auth.Repo = owner, name
	}
	if auth.Owner == "" || auth.Repo == "" {
		return auth, errors.New("--repo owner/name is required the first time you pull on a machine")
	}
	if auth.Path == "" {
		auth.Path = ui.DefaultBundlePath
	}

	if auth.Token == "" {
		auth.Token = os.Getenv("GITHUB_TOKEN")
	}
	if auth.Token == "" {
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			auth.Token = strings.TrimSpace(string(out))
		}
	}
	if auth.Token == "" {
		return auth, errors.New("no token: set GITHUB_TOKEN or log in with gh auth login")
	}
	return auth, nil
}

// readPassphrase asks without echoing. A pipe is accepted too, so the pull can
// be scripted, but there is no flag for the passphrase: one on a command line
// ends up in shell history.
func readPassphrase(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// One whole line: a passphrase has spaces in it, so this cannot be a
		// whitespace-delimited read.
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return "", fmt.Errorf("read passphrase: %w", err)
			}
			return "", errors.New("no passphrase on stdin")
		}
		return sc.Text(), nil
	}

	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read passphrase: %w", err)
	}
	return string(b), nil
}
