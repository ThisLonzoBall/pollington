// Package tui is the Bubble Tea front end.
package tui

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ThisLonzoBall/pollington/internal/agent"
	"github.com/ThisLonzoBall/pollington/internal/config"
	"github.com/ThisLonzoBall/pollington/internal/provider"
)

// chromeHeight is the number of rows View reserves below the transcript: the
// streaming block at its tallest, a blank line, the input and the status bar.
// A constant rather than a per-state calculation, because a transcript that
// changes height as the model streams is far more distracting than a couple of
// unused rows.
const chromeHeight = 9

// maxTranscriptLines bounds the scrollback. Sessions are long and every line is
// held in memory; 2000 is far more than anyone scrolls back through.
const maxTranscriptLines = 2000

// verbs are what the spinner says while the model works. Gerunds, because "-ing"
// reads as ongoing work where a noun reads as a stuck process, and short enough
// not to wrap in a narrow terminal.
var verbs = []string{
	"Thinking", "Pondering", "Noodling", "Percolating", "Marinating",
	"Ruminating", "Scheming", "Whittling", "Conjuring", "Untangling",
}

// verbInterval is slow on purpose. Faster than about 3s reads as flicker rather
// than deliberate personality.
const verbInterval = 4 * time.Second

type verbMsg struct{}

func nextVerb() tea.Cmd {
	return tea.Tick(verbInterval, func(time.Time) tea.Msg { return verbMsg{} })
}

var (
	userStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	botStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	statusStyle  = lipgloss.NewStyle().Faint(true)
	confirmStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
)

type replyMsg struct {
	text string
	err  error
}

// confirmReq travels from the agent's goroutine to the update loop. The agent
// blocks on reply until the user answers, which is what makes a y/n prompt
// possible from inside a tool call.
type confirmReq struct {
	action string
	detail string
	reply  chan bool
}

type Model struct {
	agent     *agent.Agent
	input     textinput.Model
	spin      spinner.Model
	verb      string
	started   time.Time
	lines     []string
	working   bool
	width     int
	confirmCh chan confirmReq
	pending   *confirmReq

	eventCh chan agent.Event

	// Plain strings, not strings.Builder: Bubble Tea passes the Model by value
	// on every update, and writing to a copied Builder panics.
	answer string // the reply so far, rendered as it streams in
	think  string // the model's reasoning so far, dimmed

	vp    viewport.Model
	ready bool // set once a WindowSizeMsg has given the viewport a size

	// cancel stops the turn currently in flight. Non-nil only while working.
	cancel context.CancelFunc

	root string // the project the agent is anchored to, shown in the header

	// The ctrl+s provider panel. While open it owns the keyboard.
	panel bool
	sel   int // highlighted row, an index into config.PresetNames
}

// activePreset names the preset the agent is currently using, or "" when it was
// pointed somewhere else by POLLINGTON_BASE_URL or the config file.
func (m Model) activePreset() string {
	for _, n := range config.PresetNames {
		if config.Presets[n].BaseURL == m.agent.BaseURL() {
			return n
		}
	}
	return ""
}

// openPanel shows the provider list with the current provider highlighted.
func (m *Model) openPanel() {
	m.panel, m.sel = true, 0
	for i, n := range config.PresetNames {
		if n == m.activePreset() {
			m.sel = i
		}
	}
}

// panelKey handles input while the provider panel is open.
func (m Model) panelKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "ctrl+s":
		m.panel = false
	case "up", "k":
		if m.sel > 0 {
			m.sel--
		}
	case "down", "j":
		if m.sel < len(config.PresetNames)-1 {
			m.sel++
		}
	case "enter":
		name := config.PresetNames[m.sel]
		m.panel = false
		if name == m.activePreset() {
			return m, nil
		}
		cfg, ok := config.ForPreset(name)
		if !ok {
			m.push(errorStyle.Render("no key ") + statusStyle.Render(
				"set "+config.KeyEnv[name]+" to switch to "+name))
			return m, nil
		}
		m.agent.SetClient(provider.New(cfg.BaseURL, cfg.APIKey, cfg.Model))
		m.push(statusStyle.Render("  switched to " + name + " (" + cfg.Model + ")"))
	}
	return m, nil
}

// panelView lists each preset with its price, one keystroke from selecting it.
func (m Model) panelView() string {
	var b strings.Builder
	b.WriteString("\n" + confirmStyle.Render("provider") +
		statusStyle.Render("   $ per million tokens") + "\n")
	b.WriteString(statusStyle.Render(fmt.Sprintf("    %-9s %-22s %7s %7s", "", "model", "in", "out")) + "\n")
	active := m.activePreset()
	for i, n := range config.PresetNames {
		cursor, mark := "  ", " "
		if i == m.sel {
			cursor = "> "
		}
		if n == active {
			mark = "●"
		}
		p := config.Presets[n]
		price := config.Prices[n]
		in, out := fmt.Sprintf("%7.2f", price.Input), fmt.Sprintf("%7.2f", price.Output)
		if price == (config.Price{}) {
			in, out = "   free", "   free"
		}
		note := ""
		if _, ok := config.ForPreset(n); !ok {
			note = "  no " + config.KeyEnv[n]
		}
		line := fmt.Sprintf("%s%s %-9s %-22s %s %s%s", cursor, mark, n, p.Model, in, out, note)
		if i == m.sel {
			line = confirmStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(statusStyle.Render("  ↑/↓ choose, enter switch, esc close") + "\n")
	return b.String()
}

// shortenHome swaps the home directory for ~ so the header stays readable in a
// narrow terminal.
func shortenHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return "~" + p[len(home):]
}

// push appends a transcript line and keeps the viewport pinned to the bottom,
// which is what you want while a session is running.
func (m *Model) push(line string) {
	m.lines = append(m.lines, line)
	if len(m.lines) > maxTranscriptLines {
		m.lines = m.lines[len(m.lines)-maxTranscriptLines:]
	}
	if m.ready {
		m.vp.SetContent(strings.Join(m.lines, "\n"))
		m.vp.GotoBottom()
	}
}

func New(a *agent.Agent, root string) Model {
	ti := textinput.New()
	ti.Placeholder = "ask pollington something"
	ti.Focus()
	ti.Prompt = "> "

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))
	sp.Style = statusStyle

	ch := make(chan confirmReq)
	// Buffered so a burst of tokens does not stall the agent between renders.
	ev := make(chan agent.Event, 256)

	// Installed here rather than in main so the channels have exactly one
	// owner. Both run on the agent's goroutine and must not touch Model state -
	// the channel is the only safe way across.
	a.Confirm = func(action, detail string) bool {
		reply := make(chan bool, 1)
		ch <- confirmReq{action: action, detail: detail, reply: reply}
		return <-reply
	}
	a.OnEvent = func(e agent.Event) { ev <- e }

	return Model{
		agent:     a,
		input:     ti,
		spin:      sp,
		verb:      verbs[0],
		confirmCh: ch,
		eventCh:   ev,
		root:      root,
		lines: []string{statusStyle.Render(
			"pollington  " + shortenHome(root) + "  esc stops a turn, ctrl+s switches provider, ctrl+c quits")},
	}
}

// rollVerb picks a new verb, never the one already showing - repeating looks
// like the rotation has stalled.
func (m Model) rollVerb() string {
	for i := 0; i < 8; i++ {
		if v := verbs[rand.Intn(len(verbs))]; v != m.verb {
			return v
		}
	}
	return m.verb
}

// waitForConfirm parks on the channel until the agent asks for permission. It
// is re-issued after every answer, which is the standard way to feed external
// events into a Bubble Tea program.
func waitForConfirm(ch chan confirmReq) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func waitForEvent(ch chan agent.Event) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		m.spin.Tick,
		waitForConfirm(m.confirmCh),
		waitForEvent(m.eventCh),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		h := msg.Height - chromeHeight
		if h < 3 {
			h = 3
		}
		if !m.ready {
			m.vp = viewport.New(msg.Width, h)
			m.ready = true
		} else {
			m.vp.Width, m.vp.Height = msg.Width, h
		}
		m.vp.SetContent(strings.Join(m.lines, "\n"))
		m.vp.GotoBottom()

		// textinput truncates its placeholder to a single character while Width
		// is 0, so an unsized input shows "> a" instead of the prompt text.
		if w := msg.Width - 4; w > 0 {
			m.input.Width = w
		}
		return m, nil

	case confirmReq:
		m.pending = &msg
		return m, nil

	case agent.Event:
		switch msg.Kind {
		case agent.EventReasoning:
			m.think += msg.Text
		case agent.EventContent:
			m.answer += msg.Text
		case agent.EventToolStart:
			// A real action beats a whimsical verb: "Reading main.go" tells the
			// user more than "Percolating" ever will.
			m.push(statusStyle.Render("  ⚙ " + msg.Text))
			if name, _, ok := strings.Cut(msg.Text, " "); ok {
				m.verb = "Running " + name
			}
			// A tool call means the previous answer fragment is finished.
			m.answer, m.think = "", ""
		case agent.EventToolDone:
			m.verb = m.rollVerb()
		}
		return m, waitForEvent(m.eventCh)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case verbMsg:
		if !m.working {
			return m, nil // stop rotating once the turn is over
		}
		m.verb = m.rollVerb()
		return m, nextVerb()

	case tea.KeyMsg:
		// A pending confirmation swallows all input until it is answered,
		// otherwise a stray keystroke would land in the text box while the
		// agent sits blocked waiting for an answer.
		if m.pending != nil {
			return m.answerConfirm(msg)
		}
		if m.panel {
			return m.panelKey(msg)
		}
		// Switching mid-turn would change provider under a request in flight.
		if msg.Type == tea.KeyCtrlS && !m.working {
			m.openPanel()
			return m, nil
		}
		// Scrolling keys belong to the transcript, not the text box. Routed
		// explicitly rather than by handing every key to the viewport, which
		// would swallow ordinary typing.
		if m.ready && isScrollKey(msg) {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}

		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit

		case tea.KeyEsc:
			// Esc deliberately never quits. Losing a whole conversation to a
			// stray keypress is worse than any benefit of a second quit key,
			// and the reason this exists is to stop a turn *without* that.
			if m.working && m.cancel != nil {
				m.cancel()
			}
			return m, nil
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.working {
				return m, nil
			}
			m.input.Reset()
			m.push(userStyle.Render("you ") + text)
			m.working = true
			m.started = time.Now()
			m.verb = m.rollVerb()
			m.answer, m.think = "", ""

			ctx, cancel := context.WithCancel(context.Background())
			m.cancel = cancel
			return m, tea.Batch(m.ask(ctx, text), nextVerb())
		}

	case replyMsg:
		m.working = false
		if m.cancel != nil {
			m.cancel() // release the context even on the happy path
			m.cancel = nil
		}
		// The streamed text has already been on screen; commit the authoritative
		// copy to the transcript and clear the live buffers so it is not shown
		// twice.
		m.answer, m.think = "", ""
		switch {
		case errors.Is(msg.err, context.Canceled):
			// Not a failure - the user asked for this.
			m.push(statusStyle.Render("  stopped"))
		case msg.err != nil:
			m.push(errorStyle.Render("error ") + msg.err.Error())
		default:
			m.push(botStyle.Render("pollington ") + msg.text)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// answerConfirm resolves a pending permission request. Anything that is not an
// explicit yes counts as no.
func (m Model) answerConfirm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	var ok bool
	switch strings.ToLower(key.String()) {
	case "y":
		ok = true
	case "n", "esc", "ctrl+c":
		ok = false
	default:
		return m, nil // ignore anything else and keep asking
	}

	m.pending.reply <- ok
	verdict, style := "denied", errorStyle
	if ok {
		verdict, style = "allowed", confirmStyle
	}
	m.push(style.Render(verdict+" ") +
		statusStyle.Render(m.pending.action+": "+m.pending.detail))
	m.pending = nil

	return m, waitForConfirm(m.confirmCh)
}

// ask runs the agent off the UI goroutine. Bubble Tea delivers the result back
// as a replyMsg, while progress arrives separately over eventCh.
func (m Model) ask(ctx context.Context, text string) tea.Cmd {
	return func() tea.Msg {
		out, err := m.agent.Send(ctx, text)
		return replyMsg{text: out, err: err}
	}
}

// isScrollKey reports whether a keypress should scroll the transcript. Arrow
// keys are deliberately excluded: they move the cursor inside the input, and
// stealing them would be worse than not scrolling.
func isScrollKey(k tea.KeyMsg) bool {
	switch k.Type {
	case tea.KeyPgUp, tea.KeyPgDown, tea.KeyCtrlU, tea.KeyCtrlD, tea.KeyHome, tea.KeyEnd:
		return true
	}
	return false
}

func (m Model) View() string {
	var b strings.Builder
	if m.ready {
		b.WriteString(m.vp.View() + "\n")
	} else {
		// Before the first WindowSizeMsg there is no viewport to render into.
		// Printing the lines directly keeps the very first frame from being
		// blank, and keeps the model testable without a terminal.
		for _, l := range m.lines {
			b.WriteString(l + "\n")
		}
	}

	switch {
	case m.pending != nil:
		b.WriteString("\n" + confirmStyle.Render("allow "+m.pending.action+"?") + "\n")
		b.WriteString("  " + m.pending.detail + "\n")
		b.WriteString(statusStyle.Render("  y to allow, n to decline") + "\n")
		return b.String()
	case m.working:
		// Reasoning first, dimmed, and only the tail of it. A thinking model can
		// emit thousands of tokens here; the point is to prove it is working, not
		// to make the user read it.
		if m.think != "" {
			b.WriteString(statusStyle.Render(tailLines(m.think, thinkTailLines)) + "\n")
		}
		if m.answer != "" {
			b.WriteString(botStyle.Render("pollington ") + m.answer + "\n")
		}
		// The elapsed counter is the part that earns its place: at 40 seconds a
		// static string is indistinguishable from a crash, and people kill the
		// process - wasting every token already spent on the turn.
		b.WriteString(fmt.Sprintf("%s %s… %s\n",
			m.spin.View(),
			m.verb,
			statusStyle.Render(time.Since(m.started).Truncate(time.Second).String()),
		))
	}

	if m.panel {
		b.WriteString(m.panelView())
		return b.String()
	}

	b.WriteString("\n" + m.input.View() + "\n")

	u := m.agent.Usage
	b.WriteString(statusStyle.Render(fmt.Sprintf("in %d  out %d", u.PromptTokens, u.CompletionTokens)))
	return b.String()
}

// thinkTailLines is how much of the reasoning stream stays on screen.
const thinkTailLines = 3

// tailLines keeps the last n lines of s, so a long reasoning trace scrolls in
// place instead of pushing the input box off the bottom of the terminal.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
