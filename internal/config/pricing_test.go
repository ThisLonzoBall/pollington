package config

import "testing"

func TestPriceForMatchesVersionedModelIDs(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		// The preset asks for a version the rate table does not spell out.
		{"MiniMax-M2.7", true},
		{"minimax-m2", true},
		{"MINIMAX-M2-20260101", true},
		{"openai/gpt-oss-120b", true},
		// Unlisted models must come back unknown rather than fall back to
		// something plausible-looking.
		{"deepseek-chat", false},
		{"qwen3-coder:30b", false},
		{"", false},
	} {
		if got := PriceFor(tc.model).Known(); got != tc.want {
			t.Errorf("PriceFor(%q).Known() = %v, want %v", tc.model, got, tc.want)
		}
	}
}

// TestPriceForPrefersTheLongestPrefix guards the lookup rule: adding a specific
// entry beside a general one must not depend on map iteration order.
func TestPriceForPrefersTheLongestPrefix(t *testing.T) {
	prev := prices
	prices = map[string]Pricing{
		"model":       {Input: 1, Output: 1},
		"model-turbo": {Input: 9, Output: 9},
	}
	t.Cleanup(func() { prices = prev })

	for i := 0; i < 50; i++ {
		if got := PriceFor("Model-Turbo-2026").Input; got != 9 {
			t.Fatalf("input rate = %v, want the more specific entry (9)", got)
		}
	}
}

func TestCostIsPerMillionTokens(t *testing.T) {
	p := Pricing{Input: 0.255, Output: 1.02}

	if got := p.Cost(1_000_000, 0); got != 0.255 {
		t.Errorf("a million input tokens = %v, want 0.255", got)
	}
	if got := p.Cost(0, 1_000_000); got != 1.02 {
		t.Errorf("a million output tokens = %v, want 1.02", got)
	}
	if got := p.Cost(0, 0); got != 0 {
		t.Errorf("nothing spent should cost nothing: %v", got)
	}
	// Output is the expensive side, and a thinking model's reasoning is billed
	// there - mixing the two rates up understates a turn by about 4x.
	if p.Cost(1000, 1000) != (0.255+1.02)/1000 {
		t.Error("input and output rates are being applied to the wrong counts")
	}
}

func TestConfigPriceOverridesTheTable(t *testing.T) {
	c := Config{Model: "MiniMax-M2.7"}
	if got := c.Price().Input; got != 0.255 {
		t.Errorf("without an override the table should apply, got %v", got)
	}

	// A published rate that has moved must be fixable without a code change.
	c.InputPrice, c.OutputPrice = 0.1, 0.2
	if got := c.Price(); got.Input != 0.1 || got.Output != 0.2 {
		t.Errorf("override ignored: %+v", got)
	}
}

// TestPriceOverrideSurvivesTheConfigFile: the rates are only useful if they
// persist, and omitempty on a float is easy to get wrong.
func TestPriceOverrideSurvivesTheConfigFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Save(Config{Model: "my-model", InputPrice: 0.5, OutputPrice: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Price() != (Pricing{Input: 0.5, Output: 2}) {
		t.Errorf("rates did not round-trip through the config file: %+v", got.Price())
	}
}
