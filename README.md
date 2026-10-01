# pollington

A terminal coding agent. Go + Bubble Tea, talking to any OpenAI-compatible API.

> It can read your code, change it, run your build, and check its own work.
> Streaming and the settings panel are not there yet.

## Status

| Piece | State |
|---|---|
| OpenAI-compatible client | works, non-streaming |
| Tool-calling loop | works, 25-step cap |
| `read_file`, `list_dir` | works |
| `write_file`, `run_shell` | works, each gated behind a `y/n` prompt |
| Tests | 28, no API key or network needed |
| Bubble Tea UI | input, transcript, confirm prompt, token counter |
| Streaming | TODO — biggest remaining win |
| Settings panel (ctrl+s) | TODO |

Never yet pointed at a real provider — every test runs against a fake server.

## Run

Needs Go 1.23 or newer — a dependency requires it, so the version in Ubuntu 24.04's
apt (1.22) will not build this. Built and verified against Go 1.27.1.

```sh
export MINIMAX_API_KEY=sk-...
go run .
```

Without a key it exits 1 with a message rather than failing at the first request.

## Design

One idea holds the codebase together: **the only provider-specific thing is a
base URL.** `internal/provider` speaks the OpenAI `/chat/completions` format,
which MiniMax, Ollama, llama.cpp, vLLM, Groq and OpenRouter all implement. The
agent loop, the tools and the UI never learn which one is behind it.

Presets live in `internal/config`:

| Preset | Base URL | Model |
|---|---|---|
| `minimax` | `https://api.minimax.io/v1` | `MiniMax-M2.7` |
| `deepseek` | `https://api.deepseek.com/v1` | `deepseek-chat` |
| `groq` | `https://api.groq.com/openai/v1` | `openai/gpt-oss-120b` |
| `ollama` | `http://localhost:11434/v1` | `qwen3-coder:30b` |

Override either for one run without editing anything:

```sh
POLLINGTON_BASE_URL=http://localhost:11434/v1 POLLINGTON_MODEL=qwen3-coder:30b pollington
```

Model IDs move fast — confirm the current one in your provider's console.

### Five things the code is deliberately careful about

**Reasoning content is kept in history.** MiniMax's models think between tool
calls and need that trace replayed on later requests. Strip it and the model
loses the thread mid-task — it will re-read the same file every turn.
`provider.Message` carries `reasoning_content` and `agent.Send` appends the
assistant message whole. `TestReasoningSurvivesInHistory` guards it.

**The tool list is sorted and built once.** Go randomizes map iteration order.
Deriving the tool block from a map per request would reorder the front of the
prompt every time and miss the provider's prompt cache on every turn — costing
~8x on input tokens and the prefill latency a cache hit saves, while breaking
nothing visibly. `TestToolOrderIsStable` guards it.

**Tool output is capped at 8k, head and tail.** Whatever a tool returns enters
history permanently and is resent every turn, so an unbounded build log is a
recurring charge. Truncation keeps both ends, because compilers put the summary
on the last line, and it cuts on line boundaries so a multi-byte character is
never split — `encoding/json` would silently corrupt it rather than erroring.

**`write_file` and `run_shell` need permission.** Both set `NeedsConfirm`, and a
nil confirmation handler denies rather than allows, so non-interactive callers
fail closed. `safePath` keeps file tools inside the working directory; a shell
command cannot be sandboxed that way, which is exactly why it asks first.

**`run_shell` kills the whole process group.** `exec.CommandContext` only kills
the shell, and a surviving grandchild holds the output pipe open — so
`CombinedOutput` blocks past the deadline and the timeout does nothing. Found by
`TestRunShellTimesOut`, which took 30 seconds before the fix and 0.16 after.

**Errors name the URL they used.** `api.minimax.io` (global) and
`api.minimaxi.com` (China) are separate platforms with separate keys, and the
wrong pairing returns a generic auth failure. Printing the URL turns that into
a five-second fix.

## Tests

```sh
go test ./...          # 28 tests, no API key, no network, no cost
go test -race ./...    # the confirm prompt crosses goroutines
```

Every test runs against an `httptest` server that replays canned model
responses, so the loop, the reasoning-retention rule, the cache ordering and the
permission gate are all verified offline.

## Layout

```
main.go                       wiring
internal/config/              settings, presets, ~/.config/pollington/config.json
internal/provider/            OpenAI-compatible HTTP client
internal/agent/agent.go       the tool-calling loop, truncation, the confirm gate
internal/agent/tools.go       read_file, list_dir, write_file, run_shell
internal/agent/shell_unix.go  process-group kill (and shell_other.go elsewhere)
internal/tui/                 Bubble Tea front end
```
