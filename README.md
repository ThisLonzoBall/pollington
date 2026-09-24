# pollington

A terminal coding agent. Go + Bubble Tea, talking to any OpenAI-compatible API.

> Skeleton stage. The loop, the client and the config are real; streaming, the
> settings panel and the write/shell tools are not there yet.

## Status

| Piece | State |
|---|---|
| OpenAI-compatible client | works, non-streaming |
| Tool-calling loop | works |
| `read_file`, `list_dir` | works |
| Bubble Tea UI | minimal — input, transcript, token counter |
| Streaming | TODO |
| `write_file`, `run_shell` | TODO, both need a confirm prompt |
| Settings panel (ctrl+s) | TODO |

## Run

Go is not installed on the machine this was written on, so **this has not been
compiled yet**. First run will need:

```sh
go mod tidy          # pins the charmbracelet versions
export MINIMAX_API_KEY=sk-...
go run .
```

## Design

One idea holds the codebase together: **the only provider-specific thing is a
base URL.** `internal/provider` speaks the OpenAI `/chat/completions` format,
which MiniMax, Ollama, llama.cpp, vLLM, Groq and OpenRouter all implement. The
agent loop, the tools and the UI never learn which one is behind it.

Presets live in `internal/config`:

| Preset | Base URL | Model |
|---|---|---|
| `minimax` | `https://api.minimax.io/v1` | `MiniMax-M2` |
| `ollama` | `http://localhost:11434/v1` | `qwen3-coder:30b` |
| `groq` | `https://api.groq.com/openai/v1` | `openai/gpt-oss-120b` |

### Three things the code is deliberately careful about

**Reasoning content is kept in history.** MiniMax-M2 thinks between tool calls
and needs that trace replayed on later requests. Strip it and the model loses
the thread mid-task — it will re-read the same file every turn. `provider.Message`
carries `reasoning_content` and `agent.Send` appends the assistant message whole.

**Tool output is truncated at 8k.** Whatever a tool returns enters history
permanently and is resent on every subsequent turn, so an unbounded build log
is a recurring charge rather than a one-off.

**Errors name the URL they used.** `api.minimax.io` (global) and
`api.minimaxi.com` (China) are separate platforms with separate keys, and the
wrong pairing returns a generic auth failure. Printing the URL turns that into
a five-second fix.

## Layout

```
main.go                     wiring
internal/config/            settings, presets, ~/.config/pollington/config.json
internal/provider/          OpenAI-compatible HTTP client
internal/agent/             the tool-calling loop
internal/agent/tools.go     tool implementations
internal/tui/               Bubble Tea front end
```
