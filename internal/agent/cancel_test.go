package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThisLonzoBall/pollington/internal/provider"
)

// assertHistoryValid checks the invariant the API enforces: every tool_call in
// an assistant message must have a matching tool result later in the history.
// Break it and the *next* request is rejected - so a cancellation that corrupts
// history does not fail now, it fails confusingly one turn later.
func assertHistoryValid(t *testing.T, h []provider.Message) {
	t.Helper()
	for i, m := range h {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			var answered bool
			for _, later := range h[i+1:] {
				if later.Role == "tool" && later.ToolCallID == tc.ID {
					answered = true
					break
				}
			}
			if !answered {
				t.Errorf("tool_call %q (%s) has no result: the next request would be rejected",
					tc.ID, tc.Function.Name)
			}
		}
	}
}

// TestCancelDuringModelCall cancels while the response is still streaming.
func TestCancelDuringModelCall(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: "+chunk(map[string]any{"reasoning_content": "thinking..."})+"\n\n")
		f.Flush()
		close(started)
		<-r.Context().Done() // hang until the client gives up
	}))
	defer srv.Close()

	a := New(provider.New(srv.URL, "k", "m"), []Tool{echoTool()})
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		<-started
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := a.Send(ctx, "go")
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after cancellation")
	}

	assertHistoryValid(t, a.history)
	// The user's message should survive, so continuing the conversation still
	// shows the model what was asked.
	if last := a.history[len(a.history)-1]; last.Role != "user" {
		t.Errorf("history ends with role %q, want the user's message intact", last.Role)
	}
}

// TestCancelDuringToolRun is the dangerous case: the assistant message with its
// tool_calls is already in history when the cancellation lands.
func TestCancelDuringToolRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	blocking := Tool{
		Def: provider.Tool{Type: "function", Function: provider.ToolFunction{
			Name: "slow", Description: "s", Parameters: schema(nil),
		}},
		Run: func(c context.Context, _ json.RawMessage) (string, error) {
			cancel() // the user hits Esc while this is running
			<-c.Done()
			return "", c.Err()
		},
	}

	f := newFakeAPI(t,
		toolCallReply("call_1", "slow", `{}`, "about to be interrupted"),
		answerReply("unreachable"),
	)
	a := f.agent([]Tool{blocking})

	done := make(chan struct{})
	go func() { a.Send(ctx, "go"); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Send did not return after cancellation")
	}

	// This is the assertion that matters.
	assertHistoryValid(t, a.history)

	var toolResult string
	for _, m := range a.history {
		if m.Role == "tool" {
			toolResult = m.Content
		}
	}
	if toolResult == "" {
		t.Fatal("the interrupted tool call got no result at all")
	}
	if !strings.Contains(toolResult, "cancel") && !strings.Contains(toolResult, "context") {
		t.Errorf("tool result = %q, should say it was interrupted", toolResult)
	}
}

// TestCancelKillsRunningShellCommand: Esc should stop a runaway command, not
// just stop waiting for it.
func TestCancelKillsRunningShellCommand(t *testing.T) {
	chdir(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	out, err := runShell().Run(ctx, json.RawMessage(`{"command":"sleep 30"}`))
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("took %s - cancellation did not kill the command", elapsed)
	}
	t.Logf("returned after %s: err=%v out=%q", elapsed, err, strings.TrimSpace(out))
}

// TestShellDistinguishesTimeoutFromInterrupt: both leave a dead process, but
// they mean different things to the model reading the result.
func TestShellDistinguishesTimeoutFromInterrupt(t *testing.T) {
	chdir(t, t.TempDir())

	t.Run("user interrupt", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		go func() { time.Sleep(150 * time.Millisecond); cancel() }()

		out, err := runShell().Run(ctx, json.RawMessage(`{"command":"sleep 30"}`))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !strings.Contains(out, "interrupted by the user") {
			t.Errorf("out = %q, should blame the user not the clock", out)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		old := shellTimeout
		shellTimeout = 150 * time.Millisecond
		t.Cleanup(func() { shellTimeout = old })

		out, err := runShell().Run(context.Background(), json.RawMessage(`{"command":"sleep 30"}`))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !strings.Contains(out, "timeout") {
			t.Errorf("out = %q, should name the timeout", out)
		}
	})
}
