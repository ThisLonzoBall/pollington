package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ThisLonzoBall/zippy/internal/agent"
	"github.com/ThisLonzoBall/zippy/internal/config"
	"github.com/ThisLonzoBall/zippy/internal/provider"
	"github.com/ThisLonzoBall/zippy/internal/tui"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "zippy:", err)
		os.Exit(1)
	}
	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "zippy: no API key. Set MINIMAX_API_KEY or write one to your config file.")
		os.Exit(1)
	}

	client := provider.New(cfg.BaseURL, cfg.APIKey, cfg.Model)
	a := agent.New(client, agent.DefaultTools())

	if _, err := tea.NewProgram(tui.New(a), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "zippy:", err)
		os.Exit(1)
	}
}
