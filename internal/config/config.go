// Package config loads and persists Zippy's on-disk settings.
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
var Presets = map[string]Config{
	"minimax": {BaseURL: "https://api.minimax.io/v1", Model: "MiniMax-M2"},
	"ollama":  {BaseURL: "http://localhost:11434/v1", Model: "qwen3-coder:30b", APIKey: "ollama"},
	"groq":    {BaseURL: "https://api.groq.com/openai/v1", Model: "openai/gpt-oss-120b"},
}

type Config struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

// Path returns ~/.config/zippy/config.json.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "zippy", "config.json"), nil
}

// Load reads the config file, falling back to the MiniMax preset. The
// MINIMAX_API_KEY environment variable always wins, so a key never has to
// touch the disk.
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

	if k := os.Getenv("MINIMAX_API_KEY"); k != "" {
		c.APIKey = k
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
