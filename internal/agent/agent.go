// Package agent holds the tool-calling loop. This is the whole product: ask
// the model, run whatever tools it asks for, feed the results back, repeat
// until it stops asking.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ThisLonzoBall/pollington/internal/provider"
)

const systemPrompt = `You are Pollington, a coding assistant that lives in the terminal.
Use the provided tools to inspect and edit the user's project.
Prefer reading a file before editing it. After changing code, run the build or
the tests to check your work. Keep answers short.`

// maxToolOutput caps tool results. Anything returned here lands in history
// permanently and is resent on every subsequent turn, so an unbounded build
// log is a recurring bill, not a one-off.
const maxToolOutput = 8000

// maxSteps stops a confused model from burning the budget in a loop.
const maxSteps = 25

// Tool pairs a schema with its implementation.
type Tool struct {
	Def provider.Tool
	Run func(ctx context.Context, args json.RawMessage) (string, error)

	// NeedsConfirm marks a tool that can change the user's machine. The agent
	// refuses to run it unless Confirm returns true.
	NeedsConfirm bool

	// Describe renders a pending call for the confirmation prompt, so the user
	// reads "main.go (412 bytes)" rather than a wall of escaped JSON.
	Describe func(args json.RawMessage) string
}

type Agent struct {
	client  *provider.Client
	tools   map[string]Tool
	defs    []provider.Tool // built once, sorted - see New
	history []provider.Message
	Usage   provider.Usage

	// Confirm gates every tool with NeedsConfirm set. A nil Confirm denies
	// them, so a non-interactive caller fails closed instead of silently
	// writing files.
	Confirm func(action, detail string) bool
}

func New(c *provider.Client, tools []Tool) *Agent {
	m := make(map[string]Tool, len(tools))
	defs := make([]provider.Tool, 0, len(tools))
	for _, t := range tools {
		m[t.Def.Function.Name] = t
		defs = append(defs, t.Def)
	}

	// Sorted and built once. Go randomizes map iteration order, so deriving
	// this from the map on every request would reorder the tool block that
	// sits near the front of the prompt - missing the provider's prompt cache
	// on every single turn, which costs both tokens and prefill latency and
	// breaks nothing visibly.
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Function.Name < defs[j].Function.Name
	})

	return &Agent{
		client:  c,
		tools:   m,
		defs:    defs,
		history: []provider.Message{{Role: "system", Content: systemPrompt}},
	}
}

// Send runs one user turn to completion and returns the final reply.
//
// TODO: this should emit progress events for the TUI instead of blocking.
func (a *Agent) Send(ctx context.Context, input string) (string, error) {
	a.history = append(a.history, provider.Message{Role: "user", Content: input})

	for step := 0; step < maxSteps; step++ {
		msg, usage, err := a.client.Chat(ctx, a.history, a.defs)
		if err != nil {
			return "", err
		}
		a.Usage.PromptTokens += usage.PromptTokens
		a.Usage.CompletionTokens += usage.CompletionTokens
		a.Usage.TotalTokens += usage.TotalTokens

		// Appended whole, reasoning_content included. See provider.Message.
		a.history = append(a.history, msg)

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		for _, tc := range msg.ToolCalls {
			a.history = append(a.history, provider.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    a.runTool(ctx, tc),
			})
		}
	}
	return "", fmt.Errorf("gave up after %d steps", maxSteps)
}

// runTool never returns an error: a failure is a result the model should see
// and recover from, not something that aborts the turn.
func (a *Agent) runTool(ctx context.Context, tc provider.ToolCall) string {
	t, ok := a.tools[tc.Function.Name]
	if !ok {
		return fmt.Sprintf("error: no such tool %q", tc.Function.Name)
	}
	args := json.RawMessage(tc.Function.Arguments)

	if t.NeedsConfirm {
		if a.Confirm == nil {
			return "error: " + tc.Function.Name + " requires the user's confirmation " +
				"and no confirmation handler is attached, so it was not run"
		}
		detail := string(args)
		if t.Describe != nil {
			detail = t.Describe(args)
		}
		if !a.Confirm(tc.Function.Name, detail) {
			return "error: the user declined this action. Do not retry it - " +
				"ask them what they would prefer instead."
		}
	}

	out, err := t.Run(ctx, args)
	if err != nil {
		return "error: " + err.Error()
	}
	return truncate(out, maxToolOutput)
}

// truncate caps tool output, keeping the head *and* the tail.
//
// Two reasons it isn't a plain slice. Compilers and test runners put the
// summary on the last line ("3 tests failed"), so head-only truncation throws
// away the part that matters. And a cut at a byte offset can split a multi-byte
// character, which encoding/json silently replaces rather than rejecting - so
// the cut lands on a line boundary, and any partial character left over is
// trimmed.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}

	head := max * 6 / 10
	tail := max - head

	h := s[:head]
	if i := strings.LastIndexByte(h, '\n'); i > 0 {
		h = h[:i]
	}
	h = trimPartialRune(h)

	t := s[len(s)-tail:]
	if i := strings.IndexByte(t, '\n'); i >= 0 && i+1 < len(t) {
		t = t[i+1:]
	}
	t = dropPartialRune(t)

	omitted := strings.Count(s[len(h):len(s)-len(t)], "\n")
	return fmt.Sprintf("%s\n... %d lines omitted ...\n%s", h, omitted, t)
}

// trimPartialRune drops trailing bytes that do not form a complete UTF-8
// character. A real U+FFFD decodes with size 3, so it is left alone.
func trimPartialRune(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}

// dropPartialRune does the same at the front of a string.
func dropPartialRune(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeRuneInString(s); r == utf8.RuneError && size <= 1 {
			s = s[1:]
			continue
		}
		break
	}
	return s
}
