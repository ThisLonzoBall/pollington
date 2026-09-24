// Package tui is the Bubble Tea front end.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ThisLonzoBall/zippy/internal/agent"
)

var (
	userStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	zippyStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	statusStyle = lipgloss.NewStyle().Faint(true)
)

type replyMsg struct {
	text string
	err  error
}

type Model struct {
	agent   *agent.Agent
	input   textinput.Model
	lines   []string
	working bool
	width   int
}

func New(a *agent.Agent) Model {
	ti := textinput.New()
	ti.Placeholder = "ask zippy something"
	ti.Focus()
	ti.Prompt = "> "

	return Model{
		agent: a,
		input: ti,
		lines: []string{statusStyle.Render("zippy - ctrl+c to quit")},
	}
}

func (m Model) Init() tea.Cmd { return textinput.Blink }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width

	case tea.KeyMsg:
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
			m.lines = append(m.lines, zippyStyle.Render("zippy ")+msg.text)
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
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
	if m.working {
		b.WriteString(statusStyle.Render("thinking...") + "\n")
	}
	b.WriteString("\n" + m.input.View() + "\n")

	u := m.agent.Usage
	b.WriteString(statusStyle.Render(fmt.Sprintf("in %d  out %d", u.PromptTokens, u.CompletionTokens)))
	return b.String()
}
