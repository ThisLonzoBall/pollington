package main

import (
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ThisLonzoBall/pollington/internal/agent"
	"github.com/ThisLonzoBall/pollington/internal/config"
	"github.com/ThisLonzoBall/pollington/internal/provider"
	"github.com/ThisLonzoBall/pollington/internal/tui"
)

func main() {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(versionString(debug.ReadBuildInfo()))
		return
	}

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

	if _, err := tea.NewProgram(tui.New(a, root), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "pollington:", err)
		os.Exit(1)
	}
}

// versionString describes the running build from what the Go toolchain stamped
// into it. Installed with `go install ...@latest` that is a real module
// version; built from a checkout it is "(devel)", which on its own identifies
// nothing - so the commit is included, and whether the tree was dirty when it
// was built. That is the difference between a useful bug report and "it does
// not work on my machine".
//
// Takes the BuildInfo rather than reading it, so the formatting is testable
// without building a binary per case.
func versionString(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return "pollington (unknown version)"
	}
	v := info.Main.Version
	if v == "" {
		v = "(devel)"
	}
	// A module version already identifies the build on its own - a tag, or a
	// pseudo-version that has the commit and a +dirty marker inside it. Only
	// a bare "(devel)" needs the commit appending, and appending it to the
	// rest produces "v0.0.0-...-847108053d73+dirty (8471080, dirty)".
	if v != "(devel)" {
		return "pollington " + v
	}

	var revision string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}

	var b strings.Builder
	b.WriteString("pollington " + v)
	if revision != "" {
		if len(revision) > 7 {
			revision = revision[:7]
		}
		b.WriteString(" (" + revision)
		if dirty {
			b.WriteString(", dirty")
		}
		b.WriteString(")")
	}
	return b.String()
}
