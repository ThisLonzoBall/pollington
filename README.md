<h1 align="center">pollington</h1>

<p align="center">
  <em>A coding agent that lives in your terminal.</em><br>
  Go · Bubble Tea · any OpenAI-compatible model
</p>

<p align="center">
  <a href="https://github.com/ThisLonzoBall/pollington/actions/workflows/ci.yml">
    <img alt="CI" src="https://github.com/ThisLonzoBall/pollington/actions/workflows/ci.yml/badge.svg">
  </a>
  <img alt="Go 1.23+" src="https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go&logoColor=white">
  <img alt="63 tests" src="https://img.shields.io/badge/tests-63-brightgreen">
  <img alt="no key required to try" src="https://img.shields.io/badge/try%20it-no%20API%20key-blueviolet">
</p>

---

Ask it a question. It reads your files, edits them, runs your build, and answers —
streaming as it goes.

```
pollington - ctrl+c to quit
you why is the spinner test flaky?
  ⚙ read_file internal/tui/tui_test.go

rollVerb draws at random, so two
consecutive draws can repeat.
pollington It asserts a different verb each
⣾  Percolating… 7s

> ask pollington something
in 0  out 0
```

Anything that can change your machine asks first:

```
pollington - ctrl+c to quit
you fix the deferred unlock in server.go
  ⚙ read_file server.go

allow write_file?
  overwrite server.go (1204 bytes, 48 lines)
  y to allow, n to decline
```

> Both frames above are real `View()` output, not mock-ups.

## Try it in 30 seconds, without an API key

A scripted fake provider ships with the repo, so you can watch the whole thing
work — streaming, tool calls, the permission prompt, the token counter — with no
key, no network and nothing spent.

```sh
go run ./cmd/fakeprovider &
POLLINGTON_BASE_URL=http://localhost:8711/v1 POLLINGTON_API_KEY=unused go run .
```

Type anything and press enter. It lists the directory, reads this README, then
asks permission to run `go build` — press `y`. It is not a model and ignores
your question; everything else is real.

## With a real model

```sh
export MINIMAX_API_KEY=sk-...
go run .
```

Needs **Go 1.23 or newer** — a dependency requires it, so Ubuntu 24.04's apt
version (1.22) will not build this. Developed against Go 1.27.1. Without a key
it exits 1 with a message rather than failing at the first request.

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
Settings live at `~/.config/pollington/config.json`, mode `0600`.

## Keys

| Key | Does |
|---|---|
| `enter` | send |
| `pgup` / `pgdn`, `ctrl+u` / `ctrl+d`, `home` / `end` | scroll the transcript |
| `y` / `n` | answer a permission prompt |
| `ctrl+c` | quit |

Arrow keys are left to the input box deliberately — stealing them for scrolling
would break cursor movement while typing.

## How it works

The whole product is one loop. The model cannot touch your disk; it can only
*ask*, and the loop runs the request and feeds the result back:

```
  you ──▶ agent ──▶ provider ──▶ ☁ model
                                   │
                  ┌────────────────┘
                  ▼
          tool call, not an answer
                  │
                  ▼
          run it (ask first, if it writes or executes)
                  │
                  ▼
          append the result to history ──▶ ask again
                  │
                  ▼
          no tool call this time ──▶ that's the answer
```

That loop is about thirty-five lines in `internal/agent/agent.go`. Everything else is
the care needed to make it behave.

## Status

| Piece | State |
|---|---|
| OpenAI-compatible client | streaming (SSE) |
| Tool-calling loop | 25-step cap |
| `read_file`, `list_dir` | sandboxed to the working directory |
| `write_file`, `run_shell` | gated behind a `y/n` prompt |
| UI | live stream, scrollback, spinner + elapsed, token counter |
| Tests | 63, no API key or network |
| Settings panel (`ctrl+s`) | planned |
| Live cost per provider | planned |

**It has never been pointed at a real provider.** Every test, and the demo above,
runs against a fake server. The first live call is the remaining unknown.

## Design notes

One idea holds the codebase together: **the only provider-specific thing is a
base URL.** `internal/provider` speaks the OpenAI `/chat/completions` format,
which MiniMax, DeepSeek, Groq, OpenRouter, Ollama, llama.cpp and vLLM all
implement. The agent loop, the tools and the UI never learn which is behind it.

<details>
<summary><strong>Reasoning content is kept in history</strong> — drop it and the model loops</summary>

The API is stateless: the model's entire memory is the `messages` array you
send. A tool call records *what* it did — `read_file{"path":"tui.go"}` — but
nothing about *why*. The intent lived only in the reasoning trace.

Strip it and the next turn re-derives a plan from the evidence alone, often a
different one. The symptoms are re-reading a file it already read, abandoning a
half-finished edit, or contradicting a decision two turns old.

`provider.Message` carries `reasoning_content`, and `agent.Send` appends the
assistant message *whole* before running its tool calls.
`TestReasoningSurvivesInHistory` asserts it reaches the wire, not just memory.
</details>

<details>
<summary><strong>The tool list is sorted and built once</strong> — or the prompt cache misses every turn</summary>

Go randomizes map iteration order. Deriving the tool block from a map per
request reorders the front of the prompt every time, missing the provider's
prompt cache on every turn — roughly 8x on input tokens plus the prefill a cache
hit would skip, while breaking nothing visibly. `TestToolOrderIsStable` guards it.
</details>

<details>
<summary><strong>Reasoning streams live, and a split tag never leaks</strong></summary>

Providers deliver reasoning either in a `reasoning_content` field or wrapped in
`<think>` tags inside `content` — and a tag can straddle a frame boundary,
arriving as `"<thi"` then `"nk>"`. `thinkSplitter` holds back a trailing partial
tag until the next frame resolves it, while releasing real text immediately:
buffering reasoning until the closing tag would leave the screen blank through
the longest part of the turn, which is the problem streaming exists to solve.
</details>

<details>
<summary><strong>Tool-call arguments are reassembled by index</strong></summary>

Arguments arrive as JSON fragments across many frames, and the follow-up frames
carry neither `id` nor `name` — only the index ties them to the call they belong
to. They are accumulated by index, then sorted, and the result may still be
malformed, so every parse is checked.
</details>

<details>
<summary><strong>Tool output is capped at 8k, head and tail</strong></summary>

Whatever a tool returns enters history permanently and is resent every turn, so
an unbounded build log is a recurring charge. Truncation keeps *both* ends,
because compilers put the summary on the last line, and cuts on line boundaries
so a multi-byte character is never split — `encoding/json` would silently
corrupt it rather than erroring.
</details>

<details>
<summary><strong><code>run_shell</code> kills the whole process group</strong></summary>

`exec.CommandContext` kills only the shell. `sh -c "sleep 30"` forks, and the
grandchild inherits the output pipe — so `CombinedOutput` blocks past the
deadline and the timeout does nothing. Found by `TestRunShellTimesOut`, which
took 30 seconds before the fix and 0.16 after.
</details>

<details>
<summary><strong>Errors name the URL they used</strong></summary>

`api.minimax.io` (global) and `api.minimaxi.com` (mainland China) are separate
platforms with separate keys, and the wrong pairing returns a generic auth
failure. Printing the URL turns a twenty-minute hunt into a five-second fix.
</details>

## Tests

```sh
go test ./...          # 63 tests, no API key, no network, no cost
go test -race ./...    # the confirm prompt crosses goroutines
```

Everything runs against an `httptest` server replaying canned model responses,
so the loop, the reasoning-retention rule, the cache ordering, the permission
gate and the SSE parser are all verified offline.

One test earns a mention. **`TestEventsArriveDuringTheTurn`** has the fake server
write one frame, flush, then *block* until the agent has surfaced it. If the
client buffered the response instead of streaming it, that deadlocks. A
buffering bug is invisible in every other test, because the final answer is
byte-identical either way.

The suite is checked by mutation rather than by trusting green ticks —
re-introducing each fixed bug in a scratch copy and confirming the relevant test
fails. That is how the `run_shell` timeout bug was found in the first place.

## Layout

```
main.go                       wiring
internal/config/              settings, presets, env overrides
internal/provider/            OpenAI-compatible client, SSE, <think> splitting
internal/agent/agent.go       the loop, truncation, the permission gate
internal/agent/tools.go       read_file, list_dir, write_file, run_shell
internal/agent/shell_unix.go  process-group kill (shell_other.go elsewhere)
internal/tui/                 Bubble Tea front end
cmd/fakeprovider/             scripted endpoint for running without a key
```

## Why it exists

Terminal coding agents are a crowded field — Claude Code, Cline, Aider, and
MiniMax's own MiniMax Code all exist and are better. This one is built to
understand the machinery: how a tool-calling loop actually works, why a thinking
model's trace has to be replayed, where the tokens really go, and what breaks
when a stream is parsed carelessly. Those turn out to be the interesting parts.
