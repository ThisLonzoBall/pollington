package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ThisLonzoBall/pollington/internal/config"
)

func press(m tea.Model, k tea.KeyType) Model {
	next, _ := m.Update(tea.KeyMsg{Type: k})
	return next.(Model)
}

func TestCtrlSOpensPanelWithPrices(t *testing.T) {
	m, _ := newTestModel(t)
	m = press(m, tea.KeyCtrlS)
	if !m.panel {
		t.Fatal("ctrl+s should open the panel")
	}
	v := m.View()
	for _, want := range append([]string{"0.30", "1.20", "free"}, config.PresetNames...) {
		if !strings.Contains(v, want) {
			t.Errorf("panel missing %q:\n%s", want, v)
		}
	}
}

func TestPanelEscClosesWithoutSwitching(t *testing.T) {
	m, a := newTestModel(t)
	before := a.BaseURL()
	m = press(press(m, tea.KeyCtrlS), tea.KeyEsc)
	if m.panel || a.BaseURL() != before {
		t.Error("esc must close the panel and leave the provider alone")
	}
}

func TestPanelSwallowsTyping(t *testing.T) {
	m, _ := newTestModel(t)
	m = press(m, tea.KeyCtrlS)
	next, _ := m.Update(key("x"))
	if got := next.(Model).input.Value(); got != "" {
		t.Errorf("typing leaked into the input: %q", got)
	}
}

func TestPanelSwitchesProvider(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "gk")
	m, a := newTestModel(t)
	m = press(m, tea.KeyCtrlS)
	for config.PresetNames[m.sel] != "groq" {
		m = press(m, tea.KeyDown)
	}
	m = press(m, tea.KeyEnter)
	if m.panel {
		t.Error("enter should close the panel")
	}
	if a.BaseURL() != config.Presets["groq"].BaseURL {
		t.Errorf("agent still on %s", a.BaseURL())
	}
	if m.activePreset() != "groq" {
		t.Errorf("active = %q", m.activePreset())
	}
}

func TestPanelRefusesSwitchWithoutKey(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	m, a := newTestModel(t)
	before := a.BaseURL()
	m = press(m, tea.KeyCtrlS)
	for config.PresetNames[m.sel] != "deepseek" {
		m = press(m, tea.KeyDown)
	}
	m = press(m, tea.KeyEnter)
	if a.BaseURL() != before {
		t.Error("switched to a provider with no key")
	}
	if !strings.Contains(strings.Join(m.lines, "\n"), "DEEPSEEK_API_KEY") {
		t.Error("should tell the user which variable to set")
	}
}

func TestCtrlSIgnoredMidTurn(t *testing.T) {
	m, _ := newTestModel(t)
	m.working = true
	if press(m, tea.KeyCtrlS).panel {
		t.Error("must not switch provider under a request in flight")
	}
}

func TestPanelCtrlCStillQuits(t *testing.T) {
	m, _ := newTestModel(t)
	m = press(m, tea.KeyCtrlS)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !quits(cmd) {
		t.Error("ctrl+c must quit from the panel")
	}
}
