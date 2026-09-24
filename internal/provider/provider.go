// Package provider is a client for the OpenAI-compatible /chat/completions
// API. MiniMax, Ollama, Groq, OpenRouter, vLLM and llama.cpp all speak it, so
// this file is the only place that knows about HTTP.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []Tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
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

// Chat sends one request and returns the assistant message.
//
// TODO: streaming. The SSE deltas carry reasoning_content alongside content,
// and the TUI wants both as they arrive.
func (c *Client) Chat(ctx context.Context, msgs []Message, tools []Tool) (Message, Usage, error) {
	body, err := json.Marshal(chatRequest{
		Model:    c.Model,
		Messages: msgs,
		Tools:    tools,
		Stream:   false,
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

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Message{}, Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		// Naming the URL turns the minimax.io / minimaxi.com key mixup from a
		// twenty-minute hunt into a five-second one.
		return Message{}, Usage{}, fmt.Errorf("%s: %s: %s", url, resp.Status, bytes.TrimSpace(raw))
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Message{}, Usage{}, fmt.Errorf("decoding response: %w", err)
	}
	if out.Error != nil {
		return Message{}, Usage{}, fmt.Errorf("%s", out.Error.Message)
	}
	if len(out.Choices) == 0 {
		return Message{}, Usage{}, fmt.Errorf("no choices returned")
	}
	return out.Choices[0].Message, out.Usage, nil
}
