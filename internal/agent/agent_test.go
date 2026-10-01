package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ThisLonzoBall/pollington/internal/provider"
)

// fakeAPI stands up an OpenAI-compatible endpoint that replays canned
// responses in order, so the whole loop can be exercised without a network, an
// API key, or a cent. It records every request body for later assertions.
type fakeAPI struct {
	t        *testing.T
	replies  []string
	requests []map[string]any
	server   *httptest.Server
}

func newFakeAPI(t *testing.T, replies ...string) *fakeAPI {
	f := &fakeAPI{t: t, replies: replies}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		f.requests = append(f.requests, body)

		i := len(f.requests) - 1
		if i >= len(f.replies) {
			i = len(f.replies) - 1 // repeat the last reply forever
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, f.replies[i])
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) agent(tools []Tool) *Agent {
	return New(provider.New(f.server.URL, "test-key", "fake-model"), tools)
}

// toolCallReply is an assistant turn that asks for a tool, including the
// reasoning_content that MiniMax-style models emit between calls.
func toolCallReply(id, name, args, reasoning string) string {
	msg := map[string]any{
		"role":              "assistant",
		"reasoning_content": reasoning,
		"tool_calls": []map[string]any{{
			"id":       id,
			"type":     "function",
			"function": map[string]any{"name": name, "arguments": args},
		}},
	}
	return wrap(msg, "tool_calls")
}

func answerReply(text string) string {
	return wrap(map[string]any{"role": "assistant", "content": text}, "stop")
}

func wrap(msg map[string]any, finish string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"message": msg, "finish_reason": finish}},
		"usage":   map[string]int{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120},
	})
	return string(b)
}

// echoTool is a harmless stand-in that reports what it was given.
func echoTool() Tool {
	return Tool{
		Def: provider.Tool{Type: "function", Function: provider.ToolFunction{
			Name: "echo", Description: "echo", Parameters: schema(nil),
		}},
		Run: func(_ context.Context, raw json.RawMessage) (string, error) {
			return "echoed: " + string(raw), nil
		},
	}
}

func TestLoopRunsToolThenAnswers(t *testing.T) {
	f := newFakeAPI(t,
		toolCallReply("call_1", "echo", `{"x":1}`, "I should call echo first."),
		answerReply("all done"),
	)
	a := f.agent([]Tool{echoTool()})

	got, err := a.Send(context.Background(), "do the thing")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got != "all done" {
		t.Errorf("answer = %q, want %q", got, "all done")
	}
	if len(f.requests) != 2 {
		t.Fatalf("made %d requests, want 2", len(f.requests))
	}

	// The tool result must come back as role:"tool" carrying the originating
	// tool_call_id, or the provider rejects the next request.
	var toolMsg *provider.Message
	for i := range a.history {
		if a.history[i].Role == "tool" {
			toolMsg = &a.history[i]
		}
	}
	if toolMsg == nil {
		t.Fatal("no tool message in history")
	}
	if toolMsg.ToolCallID != "call_1" {
		t.Errorf("tool_call_id = %q, want call_1", toolMsg.ToolCallID)
	}
	if !strings.Contains(toolMsg.Content, `"x":1`) {
		t.Errorf("tool result %q did not contain the arguments", toolMsg.Content)
	}

	// Usage accumulates across every step, not just the last one.
	if a.Usage.PromptTokens != 200 || a.Usage.CompletionTokens != 40 {
		t.Errorf("usage = %+v, want 200 in / 40 out", a.Usage)
	}
}

// TestReasoningSurvivesInHistory guards the MiniMax interleaved-thinking rule:
// the reasoning trace must be replayed on later requests, or the model loses
// its own plan mid-task and starts re-reading files it has already read. This
// is the easiest thing in the codebase to regress, because dropping it breaks
// nothing visibly.
func TestReasoningSurvivesInHistory(t *testing.T) {
	const reasoning = "I should call echo first."
	f := newFakeAPI(t,
		toolCallReply("call_1", "echo", `{}`, reasoning),
		answerReply("done"),
	)
	a := f.agent([]Tool{echoTool()})

	if _, err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var found bool
	for _, m := range a.history {
		if m.ReasoningContent == reasoning {
			found = true
		}
	}
	if !found {
		t.Fatal("reasoning_content was dropped from history")
	}

	// And it must actually reach the wire on the follow-up request.
	second, _ := json.Marshal(f.requests[1])
	if !strings.Contains(string(second), reasoning) {
		t.Error("reasoning_content was not sent back to the provider")
	}
}

// TestToolOrderIsStable guards the prompt-cache fix. Go randomizes map
// iteration, so deriving the tool list from the map would reorder the front of
// the prompt on every request and miss the cache every time.
func TestToolOrderIsStable(t *testing.T) {
	tools := []Tool{runShell(), readFile(), writeFile(), listDir()}
	first := New(provider.New("http://unused", "k", "m"), tools).defs

	var names []string
	for _, d := range first {
		names = append(names, d.Function.Name)
	}
	want := []string{"list_dir", "read_file", "run_shell", "write_file"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v (sorted)", names, want)
	}

	for i := 0; i < 100; i++ {
		got := New(provider.New("http://unused", "k", "m"), tools).defs
		for j := range got {
			if got[j].Function.Name != first[j].Function.Name {
				t.Fatalf("iteration %d: order changed at %d", i, j)
			}
		}
	}
}

func TestConfirmGate(t *testing.T) {
	gated := Tool{
		NeedsConfirm: true,
		Def: provider.Tool{Type: "function", Function: provider.ToolFunction{
			Name: "danger", Description: "d", Parameters: schema(nil),
		}},
		Describe: func(json.RawMessage) string { return "something destructive" },
		Run:      func(context.Context, json.RawMessage) (string, error) { return "ran", nil },
	}

	cases := []struct {
		name    string
		confirm func(string, string) bool
		wantRun bool
	}{
		{"nil handler denies", nil, false},
		{"declined", func(string, string) bool { return false }, false},
		{"allowed", func(string, string) bool { return true }, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAPI(t,
				toolCallReply("call_1", "danger", `{}`, ""),
				answerReply("ok"),
			)
			a := f.agent([]Tool{gated})
			a.Confirm = tc.confirm

			if _, err := a.Send(context.Background(), "go"); err != nil {
				t.Fatalf("Send: %v", err)
			}

			var result string
			for _, m := range a.history {
				if m.Role == "tool" {
					result = m.Content
				}
			}
			if ran := result == "ran"; ran != tc.wantRun {
				t.Errorf("tool result = %q, wantRun = %v", result, tc.wantRun)
			}
		})
	}
}

// TestConfirmDetailUsesDescribe checks the user sees a readable summary rather
// than raw escaped JSON.
func TestConfirmDetailUsesDescribe(t *testing.T) {
	var gotDetail string
	f := newFakeAPI(t,
		toolCallReply("call_1", "write_file", `{"path":"x.txt","content":"hi\nthere"}`, ""),
		answerReply("ok"),
	)
	a := f.agent([]Tool{writeFile()})
	a.Confirm = func(_, detail string) bool { gotDetail = detail; return false }

	if _, err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(gotDetail, "x.txt") || !strings.Contains(gotDetail, "bytes") {
		t.Errorf("detail = %q, want a path and a byte count", gotDetail)
	}
}

func TestToolErrorIsReportedNotFatal(t *testing.T) {
	boom := Tool{
		Def: provider.Tool{Type: "function", Function: provider.ToolFunction{
			Name: "boom", Description: "b", Parameters: schema(nil),
		}},
		Run: func(context.Context, json.RawMessage) (string, error) {
			return "", fmt.Errorf("disk on fire")
		},
	}
	f := newFakeAPI(t,
		toolCallReply("call_1", "boom", `{}`, ""),
		answerReply("recovered"),
	)
	a := f.agent([]Tool{boom})

	got, err := a.Send(context.Background(), "go")
	if err != nil {
		t.Fatalf("a tool error must not abort the turn: %v", err)
	}
	if got != "recovered" {
		t.Errorf("answer = %q, want recovered", got)
	}

	var result string
	for _, m := range a.history {
		if m.Role == "tool" {
			result = m.Content
		}
	}
	if !strings.Contains(result, "disk on fire") {
		t.Errorf("tool message %q should carry the error for the model to read", result)
	}
}

func TestUnknownToolIsReported(t *testing.T) {
	f := newFakeAPI(t,
		toolCallReply("call_1", "nope", `{}`, ""),
		answerReply("ok"),
	)
	a := f.agent([]Tool{echoTool()})

	if _, err := a.Send(context.Background(), "go"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	for _, m := range a.history {
		if m.Role == "tool" && strings.Contains(m.Content, "no such tool") {
			return
		}
	}
	t.Error("expected a 'no such tool' result the model can recover from")
}

// TestMaxStepsTerminates points the agent at a server that asks for a tool
// forever. Without the cap this would run until the budget ran out.
func TestMaxStepsTerminates(t *testing.T) {
	f := newFakeAPI(t, toolCallReply("call_1", "echo", `{}`, "again"))
	a := f.agent([]Tool{echoTool()})

	_, err := a.Send(context.Background(), "go")
	if err == nil {
		t.Fatal("expected an error after exhausting maxSteps")
	}
	if !strings.Contains(err.Error(), "gave up") {
		t.Errorf("err = %v, want a 'gave up' message", err)
	}
	if len(f.requests) != maxSteps {
		t.Errorf("made %d requests, want exactly maxSteps (%d)", len(f.requests), maxSteps)
	}
}

func TestAPIErrorSurfacesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()

	a := New(provider.New(srv.URL, "bad", "m"), []Tool{echoTool()})
	_, err := a.Send(context.Background(), "go")
	if err == nil {
		t.Fatal("expected an error")
	}
	// Naming the URL is what turns the minimax.io / minimaxi.com key mixup from
	// a twenty-minute hunt into a five-second one.
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("err = %v, should name the URL it called", err)
	}
}

// TestEndToEndWriteThenVerify is the whole product in one test: the model asks
// to write a file, then asks to run a command to check its work, then answers.
// Everything is real except the model itself - real tools, real disk, real
// shell, real confirmation gate.
func TestEndToEndWriteThenVerify(t *testing.T) {
	chdir(t, t.TempDir())

	f := newFakeAPI(t,
		toolCallReply("c1", "write_file",
			`{"path":"greet.sh","content":"#!/bin/sh\necho hello from pollington\n"}`,
			"I'll write the script first."),
		toolCallReply("c2", "run_shell", `{"command":"sh greet.sh"}`,
			"Now let me check that it runs."),
		answerReply("Created greet.sh and confirmed it prints the greeting."),
	)

	var approved []string
	a := f.agent(DefaultTools())
	a.Confirm = func(action, detail string) bool {
		approved = append(approved, action+": "+detail)
		return true
	}

	got, err := a.Send(context.Background(), "write a greeting script and check it works")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(got, "greet.sh") {
		t.Errorf("answer = %q", got)
	}

	// The file is really on disk.
	onDisk, err := os.ReadFile("greet.sh")
	if err != nil {
		t.Fatalf("greet.sh was not written: %v", err)
	}
	if !strings.Contains(string(onDisk), "hello from pollington") {
		t.Errorf("contents = %q", onDisk)
	}

	// The shell really ran it, and the output came back to the model.
	var sawOutput bool
	for _, m := range a.history {
		if m.Role == "tool" && strings.Contains(m.Content, "hello from pollington") {
			sawOutput = true
		}
	}
	if !sawOutput {
		t.Error("the command output never reached the conversation")
	}

	// Both destructive steps asked permission, in order.
	if len(approved) != 2 {
		t.Fatalf("asked for %d confirmations, want 2: %v", len(approved), approved)
	}
	if !strings.HasPrefix(approved[0], "write_file") || !strings.HasPrefix(approved[1], "run_shell") {
		t.Errorf("confirmation order = %v", approved)
	}
	if !strings.Contains(approved[0], "create") {
		t.Errorf("write prompt should say it creates a new file: %q", approved[0])
	}
}

func TestTruncate(t *testing.T) {
	t.Run("short input is untouched", func(t *testing.T) {
		if got := truncate("hello", 100); got != "hello" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("keeps head and tail", func(t *testing.T) {
		var b strings.Builder
		b.WriteString("FIRST LINE\n")
		for i := 0; i < 500; i++ {
			b.WriteString("middle filler line that is here only to take up room\n")
		}
		b.WriteString("LAST LINE")

		got := truncate(b.String(), 400)
		if len(got) > 500 {
			t.Errorf("result is %d bytes, cap was 400", len(got))
		}
		if !strings.Contains(got, "FIRST LINE") {
			t.Error("lost the head")
		}
		if !strings.Contains(got, "LAST LINE") {
			t.Error("lost the tail - this is where build failures report")
		}
		if !strings.Contains(got, "lines omitted") {
			t.Error("no truncation marker")
		}
	})

	t.Run("never splits a multi-byte character", func(t *testing.T) {
		// Pure multi-byte content with no newlines is the worst case: the cut
		// has to fall back to a byte offset.
		s := strings.Repeat("世界", 2000)
		for _, max := range []int{101, 200, 333, 1001} {
			got := truncate(s, max)
			if !utf8.ValidString(got) {
				t.Errorf("max=%d produced invalid UTF-8", max)
			}
		}
	})

	t.Run("a real replacement character is preserved", func(t *testing.T) {
		s := "start\n" + strings.Repeat("x\n", 500) + "� end"
		got := truncate(s, 200)
		if !strings.Contains(got, "�") {
			t.Error("a legitimate U+FFFD was stripped")
		}
	})
}
