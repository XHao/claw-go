package config

import "testing"

func TestApplyEnvOverrides_anthropicKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-key")
	t.Setenv("OPENAI_API_KEY", "")

	cfg := &Config{
		Models: map[string]ProviderConfig{
			"claude": {Type: "anthropic", APIKey: "old-key", Model: "claude-opus-4-5"},
			"gpt":    {Type: "openai", APIKey: "gpt-key", Model: "gpt-4o"},
		},
	}
	applyEnvOverrides(cfg)

	if cfg.Models["claude"].APIKey != "sk-ant-test-key" {
		t.Fatalf("claude api_key: got %q", cfg.Models["claude"].APIKey)
	}
	if cfg.Models["gpt"].APIKey != "gpt-key" {
		t.Fatalf("gpt api_key should be unchanged: got %q", cfg.Models["gpt"].APIKey)
	}
}

func TestApplyEnvOverrides_bothKeys_anthropicWins(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-key")

	cfg := &Config{
		Models: map[string]ProviderConfig{
			"claude": {Type: "anthropic", APIKey: "old-ant", Model: "claude-opus-4-5"},
			"gpt":    {Type: "openai", APIKey: "old-gpt", Model: "gpt-4o"},
		},
	}
	applyEnvOverrides(cfg)

	// anthropic model: ANTHROPIC_API_KEY wins over OPENAI_API_KEY
	if cfg.Models["claude"].APIKey != "sk-ant-key" {
		t.Fatalf("claude api_key: got %q, want %q", cfg.Models["claude"].APIKey, "sk-ant-key")
	}
	// openai model: OPENAI_API_KEY applies
	if cfg.Models["gpt"].APIKey != "sk-openai-key" {
		t.Fatalf("gpt api_key: got %q, want %q", cfg.Models["gpt"].APIKey, "sk-openai-key")
	}
}
