// Package agent holds the tool-calling loop. This is the whole product: ask
// the model, run whatever tools it asks for, feed the results back, repeat
// until it stops asking.
package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ThisLonzoBall/pollington/internal/provider"
)

const systemPrompt = `You are Pollington, a coding assistant that lives in the terminal.
Use the provided tools to inspect and edit the user's project.
Prefer reading a file before editing it. Keep answers short.`

// maxToolOutput caps tool results. Anything returned here lands in history
// permanently and is resent on every subsequent turn, so an unbounded build
// log is a recurring bill, not a one-off.
const maxToolOutput = 8000

// Tool pairs a schema with its implementation.
type Tool struct {
	Def provider.Tool
	Run func(ctx context.Context, args json.RawMessage) (string, error)
}

type Agent struct {
	client  *provider.Client
	tools   map[string]Tool
	history []provider.Message
	Usage   provider.Usage
}

func New(c *provider.Client, tools []Tool) *Agent {
	m := make(map[string]Tool, len(tools))
	for _, t := range tools {
		m[t.Def.Function.Name] = t
	}
	return &Agent{
		client:  c,
		tools:   m,
		history: []provider.Message{{Role: "system", Content: systemPrompt}},
	}
}

// maxSteps stops a confused model from burning the budget in a loop.
const maxSteps = 25

// Send runs one user turn to completion and returns the final reply.
//
// TODO: this should emit progress events for the TUI instead of blocking.
func (a *Agent) Send(ctx context.Context, input string) (string, error) {
	a.history = append(a.history, provider.Message{Role: "user", Content: input})

	for step := 0; step < maxSteps; step++ {
		msg, usage, err := a.client.Chat(ctx, a.history, a.toolDefs())
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
	out, err := t.Run(ctx, json.RawMessage(tc.Function.Arguments))
	if err != nil {
		return "error: " + err.Error()
	}
	if len(out) > maxToolOutput {
		out = out[:maxToolOutput] + "\n... (truncated)"
	}
	return out
}

func (a *Agent) toolDefs() []provider.Tool {
	defs := make([]provider.Tool, 0, len(a.tools))
	for _, t := range a.tools {
		defs = append(defs, t.Def)
	}
	return defs
}
