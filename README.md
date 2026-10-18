# pollington

A terminal coding agent. Go + Bubble Tea, talking to any OpenAI-compatible API.

> It can read your code, change it, run your build, and check its own work,
> streaming as it goes. The settings panel is not there yet.

## Status

| Piece | State |
|---|---|
| OpenAI-compatible client | works, streaming (SSE) |
| Tool-calling loop | works, 25-step cap |
| `read_file`, `list_dir` | works |
| `write_file`, `run_shell` | works, each gated behind a `y/n` prompt |
| Tests | 62, no API key or network needed |
| Bubble Tea UI | live stream, scrollback, spinner + elapsed, confirm prompt, token counter |
| Settings panel (ctrl+s) | TODO |

Never yet pointed at a real provider — every test runs against a fake server.

## Run

Needs Go 1.23 or newer — a dependency requires it, so the version in Ubuntu 24.04's
apt (1.22) will not build this. Built and verified against Go 1.27.1.

### Without an API key

There is a scripted fake provider in the repo, so you can see the whole thing
work — streaming, tool calls, the permission prompt, the token counter — with no
key, no network and nothing spent:

```sh
go run ./cmd/fakeprovider &
POLLINGTON_BASE_URL=http://localhost:8711/v1 POLLINGTON_API_KEY=unused go run .
```

Then ask it anything. It replies from a fixed script that walks the agent
through listing the directory, reading the README, and running `go build` —
which is where the `y/n` prompt appears. It is not a model and does not read
your question.

### With a real provider

```sh
export MINIMAX_API_KEY=sk-...
go run .
```

A new MiniMax account comes with trial credit, far more than a session of
testing costs. Without a key it exits 1 with a message rather than failing at
the first request.

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

### Keys

| Key | Does |
|---|---|
| `enter` | send |
| `pgup` / `pgdn`, `ctrl+u` / `ctrl+d`, `home` / `end` | scroll the transcript |
| `y` / `n` | answer a permission prompt |
| `ctrl+c` | quit |

Arrow keys are left to the input box deliberately — stealing them for scrolling
would break cursor movement while typing.

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

**Reasoning streams live; a split tag never leaks.** Providers deliver
reasoning either in a `reasoning_content` field or wrapped in `<think>` tags
inside `content` — and a tag can straddle a frame boundary, arriving as `"<thi"`
then `"nk>"`. `thinkSplitter` holds back a trailing partial tag until the next
frame resolves it, while releasing real text immediately: buffering reasoning
until the closing tag would leave the screen blank through the longest part of
the turn, which is the exact problem streaming exists to solve.

**Tool-call arguments are reassembled by index.** Arguments arrive as JSON
fragments across many frames, and the follow-up frames carry neither `id` nor
`name` — only the index ties them to the call they belong to.

**Errors name the URL they used.** `api.minimax.io` (global) and
`api.minimaxi.com` (China) are separate platforms with separate keys, and the
wrong pairing returns a generic auth failure. Printing the URL turns that into
a five-second fix.

## Tests

```sh
go test ./...          # 62 tests, no API key, no network, no cost
go test -race ./...    # the confirm prompt crosses goroutines
```

Every test runs against an `httptest` server that replays canned model
responses, so the loop, the reasoning-retention rule, the cache ordering, the
permission gate and the SSE parser are all verified offline.

One test deserves a mention: `TestEventsArriveDuringTheTurn` has the fake
server write one frame, flush, then **block** until the agent has surfaced it.
If the client buffered the response instead of streaming it, that deadlocks.
A buffering bug is invisible in every other test, because the final answer is
identical either way.

## Layout

```
main.go                       wiring
internal/config/              settings, presets, ~/.config/pollington/config.json
internal/provider/            OpenAI-compatible client, SSE parsing, <think> splitting
internal/agent/agent.go       the tool-calling loop, truncation, the confirm gate
internal/agent/tools.go       read_file, list_dir, write_file, run_shell
internal/agent/shell_unix.go  process-group kill (and shell_other.go elsewhere)
internal/tui/                 Bubble Tea front end
cmd/fakeprovider/             scripted endpoint for running without a key
```
