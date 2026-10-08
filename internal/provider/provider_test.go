package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stream serves a fixed event-stream body and records the request that asked
// for it.
func stream(t *testing.T, body string) (*Client, *map[string]any) {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "k", "m"), &got
}

// collect runs a stream and returns the message plus every delta emitted.
func collect(t *testing.T, body string) (Message, Usage, []Delta) {
	t.Helper()
	c, _ := stream(t, body)
	var deltas []Delta
	msg, usage, err := c.Chat(context.Background(), nil, nil, func(d Delta) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return msg, usage, deltas
}

func frames(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	return b.String() + "data: [DONE]\n\n"
}

func TestStreamAccumulatesContent(t *testing.T) {
	msg, _, deltas := collect(t, frames(
		`{"choices":[{"delta":{"role":"assistant","content":"Hello"}}]}`,
		`{"choices":[{"delta":{"content":", "}}]}`,
		`{"choices":[{"delta":{"content":"world"}}]}`,
	))

	if msg.Content != "Hello, world" {
		t.Errorf("content = %q", msg.Content)
	}
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want assistant", msg.Role)
	}
	// The point of streaming is that these arrived one at a time.
	if len(deltas) != 3 {
		t.Fatalf("got %d deltas, want 3: %+v", len(deltas), deltas)
	}
	if deltas[0].Content != "Hello" {
		t.Errorf("first delta = %q", deltas[0].Content)
	}
}

func TestStreamSeparatesReasoningField(t *testing.T) {
	msg, _, deltas := collect(t, frames(
		`{"choices":[{"delta":{"reasoning_content":"Let me think. "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"Yes."}}]}`,
		`{"choices":[{"delta":{"content":"42"}}]}`,
	))

	if msg.ReasoningContent != "Let me think. Yes." {
		t.Errorf("reasoning = %q", msg.ReasoningContent)
	}
	if msg.Content != "42" {
		t.Errorf("content = %q", msg.Content)
	}
	// Reasoning must be delivered separately so the UI can dim it.
	if deltas[0].Reasoning == "" || deltas[0].Content != "" {
		t.Errorf("first delta should be reasoning only: %+v", deltas[0])
	}
}

// TestStreamSplitsInlineThinkTags covers providers that wrap reasoning in
// <think> tags inside content rather than using a separate field.
func TestStreamSplitsInlineThinkTags(t *testing.T) {
	msg, _, _ := collect(t, frames(
		`{"choices":[{"delta":{"content":"<think>plan it</think>answer"}}]}`,
	))

	if msg.ReasoningContent != "plan it" {
		t.Errorf("reasoning = %q, want 'plan it'", msg.ReasoningContent)
	}
	if msg.Content != "answer" {
		t.Errorf("content = %q, want 'answer'", msg.Content)
	}
}

// TestStreamHandlesTagSplitAcrossFrames is the nasty case: the tag itself is cut
// in half by the chunk boundary. Naive handling leaks "<thi" into the answer.
func TestStreamHandlesTagSplitAcrossFrames(t *testing.T) {
	msg, _, deltas := collect(t, frames(
		`{"choices":[{"delta":{"content":"before <thi"}}]}`,
		`{"choices":[{"delta":{"content":"nk>secret</thin"}}]}`,
		`{"choices":[{"delta":{"content":"k>after"}}]}`,
	))

	if msg.Content != "before after" {
		t.Errorf("content = %q, want 'before after'", msg.Content)
	}
	if msg.ReasoningContent != "secret" {
		t.Errorf("reasoning = %q, want 'secret'", msg.ReasoningContent)
	}
	for _, d := range deltas {
		if strings.Contains(d.Content, "<") || strings.Contains(d.Content, "thi") {
			t.Errorf("markup leaked into a delta: %q", d.Content)
		}
	}
}

func TestStreamReassemblesToolCallArguments(t *testing.T) {
	msg, _, _ := collect(t, frames(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"ma"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"in.go\"}"}}]}}]}`,
	))

	if len(msg.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(msg.ToolCalls))
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "read_file" {
		t.Errorf("id/name lost across frames: %+v", tc)
	}
	if tc.Function.Arguments != `{"path":"main.go"}` {
		t.Errorf("arguments = %q", tc.Function.Arguments)
	}
	// And the reassembled string must actually parse.
	var args struct{ Path string }
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		t.Fatalf("reassembled arguments do not parse: %v", err)
	}
	if args.Path != "main.go" {
		t.Errorf("path = %q", args.Path)
	}
}

// TestStreamOrdersParallelToolCallsByIndex: frames for several calls interleave,
// and only the index says which is which.
func TestStreamOrdersParallelToolCallsByIndex(t *testing.T) {
	msg, _, _ := collect(t, frames(
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"second","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"first","arguments":"{}"}}]}}]}`,
	))

	if len(msg.ToolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "first" || msg.ToolCalls[1].Function.Name != "second" {
		t.Errorf("not ordered by index: %q then %q",
			msg.ToolCalls[0].Function.Name, msg.ToolCalls[1].Function.Name)
	}
}

func TestStreamReadsUsageFromFinalFrame(t *testing.T) {
	_, usage, _ := collect(t, frames(
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}`,
	))

	if usage.PromptTokens != 11 || usage.CompletionTokens != 22 || usage.TotalTokens != 33 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestStreamIgnoresKeepAlivesAndStopsAtDone(t *testing.T) {
	body := ": keep-alive ping\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"kept\"}}]}\n\n" +
		"\n" +
		"data: [DONE]\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" IGNORED\"}}]}\n\n"

	msg, _, _ := collect(t, body)
	if msg.Content != "kept" {
		t.Errorf("content = %q - anything after [DONE] must be ignored", msg.Content)
	}
}

func TestStreamSurfacesErrorFrame(t *testing.T) {
	c, _ := stream(t, frames(`{"error":{"message":"model overloaded"}}`))
	_, _, err := c.Chat(context.Background(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Errorf("err = %v, want the provider's message", err)
	}
}

func TestStreamRejectsMalformedFrame(t *testing.T) {
	c, _ := stream(t, "data: {not json\n\n")
	if _, _, err := c.Chat(context.Background(), nil, nil, nil); err == nil {
		t.Error("expected an error on an undecodable frame")
	}
}

func TestNonOKResponseNamesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "bad", "m")
	_, _, err := c.Chat(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	// api.minimax.io vs api.minimaxi.com are different platforms with different
	// keys, and the error for the wrong pairing is this generic.
	if !strings.Contains(err.Error(), srv.URL) {
		t.Errorf("err = %v, should name the URL it called", err)
	}
}

func TestNilEmitIsSafe(t *testing.T) {
	c, _ := stream(t, frames(`{"choices":[{"delta":{"content":"fine"}}]}`))
	msg, _, err := c.Chat(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if msg.Content != "fine" {
		t.Errorf("content = %q", msg.Content)
	}
}

func TestRequestAsksForStreamingAndUsage(t *testing.T) {
	c, got := stream(t, frames(`{"choices":[{"delta":{"content":"x"}}]}`))
	if _, _, err := c.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if s, _ := (*got)["stream"].(bool); !s {
		t.Error(`request did not set "stream": true`)
	}
	opts, ok := (*got)["stream_options"].(map[string]any)
	if !ok {
		t.Fatal("request did not include stream_options - usage would never arrive")
	}
	if inc, _ := opts["include_usage"].(bool); !inc {
		t.Error("include_usage was not requested")
	}
}

func TestPartialSuffix(t *testing.T) {
	for _, tc := range []struct {
		s, tag string
		want   int
	}{
		{"abc<thi", thinkOpen, 4},
		{"abc<", thinkOpen, 1},
		{"abc", thinkOpen, 0},
		{"", thinkOpen, 0},
		{"x</thin", thinkClose, 6},
		// A complete tag is not a *partial* suffix - the caller handles that
		// case by finding the index instead.
		{"<think>", thinkOpen, 0},
	} {
		if got := partialSuffix(tc.s, tc.tag); got != tc.want {
			t.Errorf("partialSuffix(%q, %q) = %d, want %d", tc.s, tc.tag, got, tc.want)
		}
	}
}

// TestThinkSplitterStreamsReasoningLive locks down the property that matters
// for the UI: reasoning is released as it arrives, not buffered until the
// closing tag. Holding it would leave the screen blank through the longest part
// of a thinking model's turn, which is the exact problem streaming exists to fix.
func TestThinkSplitterStreamsReasoningLive(t *testing.T) {
	var s thinkSplitter

	content, reasoning := s.push("<think>thinking out loud")
	if content != "" {
		t.Errorf("content = %q, want empty", content)
	}
	if reasoning != "thinking out loud" {
		t.Errorf("reasoning = %q, want it released immediately", reasoning)
	}
	if c, r := s.flush(); c != "" || r != "" {
		t.Errorf("flush = (%q, %q), want empty - nothing was pending", c, r)
	}
}

func TestThinkSplitterHoldsPartialTagThenFlushes(t *testing.T) {
	var s thinkSplitter

	// Ends on a half-written closing tag, which must not leak out as text.
	_, reasoning := s.push("<think>done</thi")
	if reasoning != "done" {
		t.Errorf("reasoning = %q, want 'done' with the partial tag withheld", reasoning)
	}

	// The stream ends without resolving it, so flush attributes the remainder
	// to whichever side we were on rather than discarding it.
	c, r := s.flush()
	if c != "" || r != "</thi" {
		t.Errorf("flush = (%q, %q), want ('', '</thi')", c, r)
	}
}
