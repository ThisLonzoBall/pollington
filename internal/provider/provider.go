// Package provider is a client for the OpenAI-compatible /chat/completions
// API. MiniMax, Ollama, Groq, OpenRouter, vLLM and llama.cpp all speak it, so
// this file is the only place that knows about HTTP.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Message is one turn of conversation.
//
// ReasoningContent matters more than it looks. MiniMax-M2 is an interleaved
// thinking model: it emits a reasoning trace between tool calls, and that
// trace must be sent back in subsequent requests. Drop it and the model loses
// the thread mid-task -- the classic symptom is re-reading the same file on
// every turn. Never strip it from history that is still in flight.
type Message struct {
	Role             string     `json:"role"` // system | user | assistant | tool
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"` // set when Role == "tool"
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name string `json:"name"`
		// Arguments is a JSON *string*, not an object, and small models do
		// emit malformed ones. Always handle the unmarshal error.
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Tool is a function definition advertised to the model.
type Tool struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema
}

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Tools         []Tool         `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

// streamOptions asks for a final usage chunk. Providers that do not recognise
// it ignore it, and the token counter simply stays at zero.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// streamChunk is one SSE frame. Everything in a delta is optional and arrives
// in fragments, which is the whole difficulty of parsing this format.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role             string           `json:"role"`
			Content          string           `json:"content"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []streamToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// streamToolCall is keyed by Index rather than ID, because ID and Name usually
// appear only in the first fragment while Arguments dribbles in across many.
type streamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Delta is an incremental slice of an assistant turn, handed to the caller as
// it arrives so a UI can render before the turn is finished.
type Delta struct {
	Content   string // visible answer text
	Reasoning string // the model's thinking
}

// Usage feeds the token counter in the status bar. Cost is invisible until you
// put it on screen.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

func New(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Chat streams one request, calling emit for each slice of text as it arrives,
// and returns the assembled assistant message once the stream closes.
//
// Streaming does not make the turn faster - it makes it *feel* faster, because
// perceived latency is time-to-first-token rather than time-to-completion. That
// matters most with a thinking model, which may emit hundreds of reasoning
// tokens before the first word of its answer.
//
// emit may be nil. It is called on the calling goroutine, so a UI callback must
// hand off over a channel rather than touching widget state.
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []Tool, emit func(Delta)) (Message, Usage, error) {
	if emit == nil {
		emit = func(Delta) {}
	}

	body, err := json.Marshal(chatRequest{
		Model:         c.Model,
		Messages:      msgs,
		Tools:         tools,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	})
	if err != nil {
		return Message{}, Usage{}, err
	}

	url := c.BaseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Message{}, Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Naming the URL turns the minimax.io / minimaxi.com key mixup from a
		// twenty-minute hunt into a five-second one. Cap the read: an error page
		// from a proxy can be enormous.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return Message{}, Usage{}, fmt.Errorf("%s: %s: %s", url, resp.Status, bytes.TrimSpace(raw))
	}

	var (
		content   strings.Builder
		reasoning strings.Builder
		split     thinkSplitter
		usage     Usage
		calls     = map[int]*ToolCall{}
		indexes   []int
	)

	sc := bufio.NewScanner(resp.Body)
	// A single frame carrying a large tool argument can exceed the 64KiB default.
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Blank lines separate frames; a leading colon is an SSE comment, which
		// some providers send as a keep-alive.
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // event:, id: and retry: are not used here
		}
		if data = strings.TrimSpace(data); data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Message{}, Usage{}, fmt.Errorf("decoding stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return Message{}, Usage{}, fmt.Errorf("%s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue // the final usage-only frame has no choices
		}
		d := chunk.Choices[0].Delta

		if d.ReasoningContent != "" {
			reasoning.WriteString(d.ReasoningContent)
			emit(Delta{Reasoning: d.ReasoningContent})
		}
		if d.Content != "" {
			// Not every provider splits reasoning into its own field; some wrap
			// it in <think> tags inside content.
			text, think := split.push(d.Content)
			content.WriteString(text)
			reasoning.WriteString(think)
			if text != "" || think != "" {
				emit(Delta{Content: text, Reasoning: think})
			}
		}
		for _, tc := range d.ToolCalls {
			cur, seen := calls[tc.Index]
			if !seen {
				cur = &ToolCall{Type: "function"}
				calls[tc.Index] = cur
				indexes = append(indexes, tc.Index)
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Type != "" {
				cur.Type = tc.Type
			}
			if tc.Function.Name != "" {
				cur.Function.Name = tc.Function.Name
			}
			cur.Function.Arguments += tc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		return Message{}, Usage{}, fmt.Errorf("reading stream: %w", err)
	}

	// Anything held back waiting on a tag that never closed.
	if text, think := split.flush(); text != "" || think != "" {
		content.WriteString(text)
		reasoning.WriteString(think)
		emit(Delta{Content: text, Reasoning: think})
	}

	msg := Message{
		Role:             "assistant",
		Content:          content.String(),
		ReasoningContent: reasoning.String(),
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		msg.ToolCalls = append(msg.ToolCalls, *calls[i])
	}
	return msg, usage, nil
}

const thinkOpen, thinkClose = "<think>", "</think>"

// thinkSplitter separates inline <think>...</think> reasoning from answer text.
//
// The complication is that a tag can straddle a chunk boundary: "<thi" arrives
// in one frame and "nk>foo" in the next. Emitting "<thi" as answer text would
// leak markup into the transcript, so a trailing partial tag is held back until
// the next chunk resolves it.
type thinkSplitter struct {
	inside bool
	buf    string
}

func (s *thinkSplitter) push(chunk string) (content, reasoning string) {
	s.buf += chunk

	var text, think strings.Builder
	for {
		// Which tag we are hunting, and where the text goes, both depend on
		// which side of a <think> block we are currently on.
		want, out := thinkOpen, &text
		if s.inside {
			want, out = thinkClose, &think
		}

		if i := strings.Index(s.buf, want); i >= 0 {
			out.WriteString(s.buf[:i])
			s.buf = s.buf[i+len(want):]
			s.inside = !s.inside
			continue
		}
		keep := partialSuffix(s.buf, want)
		out.WriteString(s.buf[:len(s.buf)-keep])
		s.buf = s.buf[len(s.buf)-keep:]
		return text.String(), think.String()
	}
}

// flush returns whatever is still held back, attributed by the current state.
func (s *thinkSplitter) flush() (content, reasoning string) {
	out := s.buf
	s.buf = ""
	if s.inside {
		return "", out
	}
	return out, ""
}

// partialSuffix returns the length of the longest proper prefix of tag that s
// ends with, so a tag split across frames is never emitted as text.
func partialSuffix(s, tag string) int {
	n := len(tag) - 1
	if len(s) < n {
		n = len(s)
	}
	for k := n; k > 0; k-- {
		if strings.HasSuffix(s, tag[:k]) {
			return k
		}
	}
	return 0
}
