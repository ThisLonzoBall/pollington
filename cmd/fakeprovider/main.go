// Command fakeprovider serves a scripted OpenAI-compatible endpoint so
// Pollington can be run and demonstrated without an API key, a network
// connection, or any spend.
//
// It is not a model. It replies from a fixed script that walks the agent
// through the whole loop - think, list a directory, read a file, run a shell
// command (which triggers the confirmation prompt), then answer - which is
// enough to exercise streaming, tool calls, permissions and the token counter.
//
//	go run ./cmd/fakeprovider &
//	POLLINGTON_BASE_URL=http://localhost:8711/v1 POLLINGTON_API_KEY=unused go run .
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
)

func main() {
	addr := flag.String("addr", "localhost:8711", "address to listen on")
	delay := flag.Duration("delay", 40*time.Millisecond, "pause between streamed frames")
	flag.Parse()

	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		// Which step of the script we are on is just how many tool results the
		// agent has sent back so far.
		var step int
		for _, m := range req.Messages {
			if m.Role == "tool" {
				step++
			}
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		send := func(frame string) {
			fmt.Fprintf(w, "data: %s\n\n", frame)
			flusher.Flush()
			time.Sleep(*delay)
		}

		for _, frame := range script(step) {
			send(frame)
		}
		send(usage(step))
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})

	log.Printf("fake provider on http://%s/v1 - point POLLINGTON_BASE_URL at it", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// script returns the frames for one step of the demo.
func script(step int) []string {
	switch step {
	case 0:
		return append(
			think("Let me see what is in this directory before I answer. "),
			toolCall("call_1", "list_dir", `{"path":"."}`)...,
		)
	case 1:
		return append(
			think("There is a README. I will read it. "),
			toolCall("call_2", "read_file", `{"path":"README.md"}`)...,
		)
	case 2:
		return append(
			think("Now let me check the project actually builds. "),
			// run_shell needs confirmation, so this is where the y/n prompt appears.
			toolCall("call_3", "run_shell", `{"command":"go build ./... && echo BUILD OK"}`)...,
		)
	default:
		return text("Had a look around: this is Pollington, a terminal coding agent " +
			"in Go. I listed the directory, read the README, and ran the build. " +
			"Everything you just saw streamed in from a scripted fake - no model " +
			"was involved and nothing was spent.")
	}
}

// think streams reasoning a few words at a time, the way an interleaved
// thinking model does between tool calls.
func think(s string) []string {
	var out []string
	for _, word := range strings.SplitAfter(s, " ") {
		if word != "" {
			out = append(out, frame(map[string]any{"reasoning_content": word}))
		}
	}
	return out
}

func text(s string) []string {
	var out []string
	for _, word := range strings.SplitAfter(s, " ") {
		if word != "" {
			out = append(out, frame(map[string]any{"content": word}))
		}
	}
	return out
}

// toolCall deliberately splits the arguments across two frames, because real
// providers fragment them and reassembly is the part that breaks.
func toolCall(id, name, args string) []string {
	cut := len(args) / 2
	return []string{
		frame(map[string]any{"tool_calls": []map[string]any{{
			"index": 0, "id": id, "type": "function",
			"function": map[string]any{"name": name, "arguments": args[:cut]},
		}}}),
		frame(map[string]any{"tool_calls": []map[string]any{{
			"index":    0,
			"function": map[string]any{"arguments": args[cut:]},
		}}}),
	}
}

func frame(delta map[string]any) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{"index": 0, "delta": delta}},
	})
	return string(b)
}

// usage is the final choices-less frame, with numbers that grow per step so the
// status bar visibly accumulates.
func usage(step int) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{},
		"usage": map[string]int{
			"prompt_tokens":     900 + step*600,
			"completion_tokens": 60 + step*20,
			"total_tokens":      960 + step*620,
		},
	})
	return string(b)
}
