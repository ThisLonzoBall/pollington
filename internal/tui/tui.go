// Package tui is the Bubble Tea front end.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ThisLonzoBall/pollington/internal/agent"
)

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
	lines     []string
	working   bool
	width     int
	confirmCh chan confirmReq
	pending   *confirmReq
}

func New(a *agent.Agent) Model {
	ti := textinput.New()
	ti.Placeholder = "ask pollington something"
	ti.Focus()
	ti.Prompt = "> "

	ch := make(chan confirmReq)

	// Installed here rather than in main so the channel has exactly one owner.
	// This runs on the agent's goroutine and must not touch Model state - the
	// only safe channel of communication is ch.
	a.Confirm = func(action, detail string) bool {
		reply := make(chan bool, 1)
		ch <- confirmReq{action: action, detail: detail, reply: reply}
		return <-reply
	}

	return Model{
		agent:     a,
		input:     ti,
		confirmCh: ch,
		lines:     []string{statusStyle.Render("pollington - ctrl+c to quit")},
	}
}

// waitForConfirm parks on the channel until the agent asks for permission. It
// is re-issued after every answer, which is the standard way to feed external
// events into a Bubble Tea program.
func waitForConfirm(ch chan confirmReq) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, waitForConfirm(m.confirmCh))
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width

	case confirmReq:
		m.pending = &msg
		return m, nil

	case tea.KeyMsg:
		// A pending confirmation swallows all input until it is answered,
		// otherwise a stray keystroke would land in the text box while the
		// agent sits blocked waiting for an answer.
		if m.pending != nil {
			return m.answerConfirm(msg)
		}
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			if text == "" || m.working {
				return m, nil
			}
			m.input.Reset()
			m.lines = append(m.lines, userStyle.Render("you ")+text)
			m.working = true
			return m, m.ask(text)
		}

	case replyMsg:
		m.working = false
		if msg.err != nil {
			m.lines = append(m.lines, errorStyle.Render("error ")+msg.err.Error())
		} else {
			m.lines = append(m.lines, botStyle.Render("pollington ")+msg.text)
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
	m.lines = append(m.lines, style.Render(verdict+" ")+
		statusStyle.Render(m.pending.action+": "+m.pending.detail))
	m.pending = nil

	return m, waitForConfirm(m.confirmCh)
}

// ask runs the agent off the UI goroutine. Bubble Tea delivers the result back
// as a replyMsg.
//
// TODO: stream tokens and tool activity instead of waiting for the whole turn.
func (m Model) ask(text string) tea.Cmd {
	return func() tea.Msg {
		out, err := m.agent.Send(context.Background(), text)
		return replyMsg{text: out, err: err}
	}
}

func (m Model) View() string {
	var b strings.Builder
	for _, l := range m.lines {
		b.WriteString(l + "\n")
	}

	switch {
	case m.pending != nil:
		b.WriteString("\n" + confirmStyle.Render("allow "+m.pending.action+"?") + "\n")
		b.WriteString("  " + m.pending.detail + "\n")
		b.WriteString(statusStyle.Render("  y to allow, n to decline") + "\n")
		return b.String()
	case m.working:
		b.WriteString(statusStyle.Render("thinking...") + "\n")
	}

	b.WriteString("\n" + m.input.View() + "\n")

	u := m.agent.Usage
	b.WriteString(statusStyle.Render(fmt.Sprintf("in %d  out %d", u.PromptTokens, u.CompletionTokens)))
	return b.String()
}
