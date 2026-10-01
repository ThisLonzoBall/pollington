// Package config loads and persists Pollington's on-disk settings.
//
// Everything provider-specific lives here. The rest of the codebase only ever
// sees a BaseURL, an APIKey and a Model, which is what makes swapping MiniMax
// for Ollama, Groq or Gemini a settings change rather than a refactor.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Presets are the providers offered in the settings panel. They all speak the
// OpenAI-compatible /chat/completions wire format.
//
// Model IDs move faster than this file does - MiniMax has shipped M2.5, M2.7
// and M3 since M2. Confirm the current ID in your provider's console; a stale
// one comes back as an unhelpful API error rather than a clear "no such model".
// Note that api.minimax.io (global) and api.minimaxi.com (mainland China) are
// separate platforms with separate keys.
var Presets = map[string]Config{
	"minimax":  {BaseURL: "https://api.minimax.io/v1", Model: "MiniMax-M2.7"},
	"deepseek": {BaseURL: "https://api.deepseek.com/v1", Model: "deepseek-chat"},
	"groq":     {BaseURL: "https://api.groq.com/openai/v1", Model: "openai/gpt-oss-120b"},
	"ollama":   {BaseURL: "http://localhost:11434/v1", Model: "qwen3-coder:30b", APIKey: "ollama"},
}

type Config struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// Path returns ~/.config/pollington/config.json.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pollington", "config.json"), nil
}

// Load resolves settings in increasing order of precedence: the MiniMax
// preset, then the config file, then the environment. Environment variables win
// so a key never has to touch the disk, and so switching provider for one run
// needs no file edit:
//
//	POLLINGTON_BASE_URL=http://localhost:11434/v1 POLLINGTON_MODEL=qwen3-coder:30b pollington
func Load() (Config, error) {
	c := Presets["minimax"]

	path, err := Path()
	if err != nil {
		return c, err
	}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parsing %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return c, err
	}

	// POLLINGTON_API_KEY is the provider-neutral name; MINIMAX_API_KEY stays
	// supported because it is what the README has always told people to set.
	for _, env := range []struct {
		name string
		dst  *string
	}{
		{"MINIMAX_API_KEY", &c.APIKey},
		{"POLLINGTON_API_KEY", &c.APIKey},
		{"POLLINGTON_BASE_URL", &c.BaseURL},
		{"POLLINGTON_MODEL", &c.Model},
	} {
		if v := os.Getenv(env.name); v != "" {
			*env.dst = v
		}
	}
	return c, nil
}

// Save writes the config with 0600 permissions, since it may hold an API key.
func Save(c Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
