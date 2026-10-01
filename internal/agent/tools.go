package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ThisLonzoBall/pollington/internal/provider"
)

// shellTimeout keeps a hung command from wedging the session. The model gets
// the partial output plus a note, which is something it can reason about. A var
// rather than a const so tests can exercise the kill path in milliseconds.
var shellTimeout = 30 * time.Second

// DefaultTools is the full set. read_file and list_dir are harmless;
// write_file and run_shell can destroy work, so both set NeedsConfirm.
func DefaultTools() []Tool {
	return []Tool{readFile(), listDir(), writeFile(), runShell()}
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

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func writeFile() Tool {
	return Tool{
		NeedsConfirm: true,
		Def: provider.Tool{
			Type: "function",
			Function: provider.ToolFunction{
				Name: "write_file",
				Description: "Create or overwrite a text file with the given content. " +
					"The whole file is replaced, so include everything that should remain.",
				Parameters: schema(map[string]any{
					"path":    map[string]any{"type": "string", "description": "Path relative to the working directory."},
					"content": map[string]any{"type": "string", "description": "The complete new contents of the file."},
				}, "path", "content"),
			},
		},
		Describe: func(raw json.RawMessage) string {
			var a writeArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return string(raw)
			}
			verb := "overwrite"
			if p, err := safePath(a.Path); err == nil {
				if _, err := os.Stat(p); os.IsNotExist(err) {
					verb = "create"
				}
			}
			return fmt.Sprintf("%s %s (%d bytes, %d lines)",
				verb, a.Path, len(a.Content), strings.Count(a.Content, "\n")+1)
		},
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a writeArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			if a.Path == "" {
				return "", fmt.Errorf("path is required")
			}
			p, err := safePath(a.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, []byte(a.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %s (%d bytes)", a.Path, len(a.Content)), nil
		},
	}
}

type shellArgs struct {
	Command string `json:"command"`
}

func runShell() Tool {
	return Tool{
		NeedsConfirm: true,
		Def: provider.Tool{
			Type: "function",
			Function: provider.ToolFunction{
				Name: "run_shell",
				Description: "Run a shell command in the working directory and return its " +
					"combined stdout and stderr. Use this to build, test, or inspect the project.",
				Parameters: schema(map[string]any{
					"command": map[string]any{"type": "string", "description": "The command to run, e.g. 'go build ./...'."},
				}, "command"),
			},
		},
		Describe: func(raw json.RawMessage) string {
			var a shellArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return string(raw)
			}
			return a.Command
		},
		// A non-zero exit is not a Go error here: the model needs to see the
		// failing output to fix it. Returning an error instead would discard
		// stdout, which is exactly the part that says what went wrong.
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var a shellArgs
			if err := json.Unmarshal(raw, &a); err != nil {
				return "", fmt.Errorf("bad arguments: %w", err)
			}
			if strings.TrimSpace(a.Command) == "" {
				return "", fmt.Errorf("command is required")
			}
			wd, err := os.Getwd()
			if err != nil {
				return "", err
			}

			ctx, cancel := context.WithTimeout(ctx, shellTimeout)
			defer cancel()

			cmd := exec.CommandContext(ctx, "sh", "-c", a.Command)
			cmd.Dir = wd

			// Kill the whole process group on timeout, not just the shell, and
			// keep WaitDelay as a backstop so a descendant holding the output
			// pipe open cannot stall CombinedOutput past the deadline.
			setProcessGroup(cmd)
			cmd.Cancel = func() error { return killGroup(cmd) }
			cmd.WaitDelay = 2 * time.Second

			out, runErr := cmd.CombinedOutput()
			res := string(out)

			switch {
			case ctx.Err() != nil:
				return res + fmt.Sprintf("\n(killed: no output for %s)", shellTimeout), nil
			case runErr != nil:
				return res + fmt.Sprintf("\n(command failed: %v)", runErr), nil
			case strings.TrimSpace(res) == "":
				return "(exit 0, no output)", nil
			}
			return res, nil
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
