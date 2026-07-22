// Command ssh-client is a TUI SSH client: a server list on the left, a live
// embedded PTY session on the right.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pyjhoop/ssh-client/internal/config"
	"github.com/pyjhoop/ssh-client/internal/ui"
)

func main() {
	store, err := config.Default()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ssh-client:", err)
		os.Exit(1)
	}

	p := tea.NewProgram(
		ui.New(store),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ssh-client:", err)
		os.Exit(1)
	}
}
