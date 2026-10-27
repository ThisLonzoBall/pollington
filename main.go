package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ThisLonzoBall/pollington/internal/agent"
	"github.com/ThisLonzoBall/pollington/internal/config"
	"github.com/ThisLonzoBall/pollington/internal/provider"
	"github.com/ThisLonzoBall/pollington/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pollington:", err)
		os.Exit(1)
	}
	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "pollington: no API key. Set MINIMAX_API_KEY or write one to your config file.")
		os.Exit(1)
	}

	// Anchor every tool at the project root rather than the directory this
	// happened to be launched from, so paths the model uses are stable and
	// shell commands run where the build expects to.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pollington:", err)
		os.Exit(1)
	}
	root := agent.FindRoot(cwd)

	client := provider.New(cfg.BaseURL, cfg.APIKey, cfg.Model)
	a := agent.New(client, agent.DefaultTools(root))

	if _, err := tea.NewProgram(tui.New(a, root, cfg.Price()), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pollington:", err)
		os.Exit(1)
	}
}
