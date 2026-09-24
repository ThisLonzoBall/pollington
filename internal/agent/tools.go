package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ThisLonzoBall/pollington/internal/provider"
)

// DefaultTools is the starting set. write_file and run_shell come next, and
// both need a confirmation prompt in the TUI before they land.
func DefaultTools() []Tool {
	return []Tool{readFile(), listDir()}
}

func schema(props map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
		"required":   required,
	}
}

func readFile() Tool {
	return Tool{
		Def: provider.Tool{
			Type: "function",
			Function: provider.ToolFunction{
				Name:        "read_file",
				Description: "Read a UTF-8 text file from the working directory.",
				Parameters: schema(map[string]any{
					"path": map[string]any{"type": "string", "description": "Path relative to the working directory."},
				}, "path"),
			},
		},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			p, err := safePath(a.Path)
			if err != nil {
				return "", err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}
}

func listDir() Tool {
	return Tool{
		Def: provider.Tool{
			Type: "function",
			Function: provider.ToolFunction{
				Name:        "list_dir",
				Description: "List the entries of a directory.",
				Parameters: schema(map[string]any{
					"path": map[string]any{"type": "string", "description": "Directory relative to the working directory. Defaults to '.'."},
				}),
			},
		},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(raw, &a)
			if a.Path == "" {
				a.Path = "."
			}
			p, err := safePath(a.Path)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for _, e := range entries {
				name := e.Name()
				if e.IsDir() {
					name += "/"
				}
				b.WriteString(name + "\n")
			}
			return b.String(), nil
		},
	}
}

// safePath keeps the model inside the working directory. A model that has
// talked itself into reading ~/.ssh/id_rsa is not a hypothetical.
func safePath(p string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(wd, p))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(wd, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the working directory", p)
	}
	return abs, nil
}
