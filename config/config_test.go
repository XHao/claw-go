package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/XHao/claw-go/config"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func writeTempJSON(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestLoadMinimal(t *testing.T) {
	path := writeTemp(t, `
provider:
  api_key: "sk-test"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxHistoryTurns != 20 {
		t.Errorf("want MaxHistoryTurns 20, got %d", cfg.MaxHistoryTurns)
	}
	if cfg.Provider.Model != "gpt-4o-mini" {
		t.Errorf("want default model, got %q", cfg.Provider.Model)
	}
	if cfg.CLI.Prompt == "" {
		t.Error("want non-empty default CLI prompt")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadUnsupportedProvider(t *testing.T) {
	path := writeTemp(t, `
provider:
  type: anthropic
  api_key: "sk-test"
`)
	_, err := config.Load(path)
	if err == nil {
		t.Error("expected error for unsupported provider type")
	}
}

func TestValidateServeRequiresAPIKey(t *testing.T) {
	path := writeTemp(t, ``)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := config.ValidateServe(cfg); err == nil {
		t.Error("expected error when api_key is missing in serve mode")
	}
}

func TestValidateServeRejectsOAuthPlaceholder(t *testing.T) {
	path := writeTemp(t, `provider: {api_key: "qwen-oauth"}`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := config.ValidateServe(cfg); err == nil {
		t.Error("expected error for OAuth placeholder api_key")
	}
}

func TestEnvOverrideAlwaysWins(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env-override")
	// Config file has a different (placeholder) key — env var must win.
	path := writeTemp(t, `provider: {api_key: "qwen-oauth"}`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider.APIKey != "sk-env-override" {
		t.Errorf("want env override %q, got %q", "sk-env-override", cfg.Provider.APIKey)
	}
	// With the env override the OAuth placeholder is gone, so serve validation passes.
	if err := config.ValidateServe(cfg); err != nil {
		t.Errorf("unexpected ValidateServe error after env override: %v", err)
	}
}

func TestSocketPathDefault(t *testing.T) {
	path := writeTemp(t, `
provider:
  api_key: "sk-test"
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// SocketPath empty in config → caller resolves via ipc.DefaultSocketPath().
	_ = cfg.SocketPath
}

// ── claw JSON format tests ────────────────────────────────────────────────

const sampleOpenClawJSON = `{
  "models": {
    "mode": "merge",
    "providers": {
      "my-openai": {
        "baseUrl": "https://api.openai.com/v1",
        "apiKey": "sk-live-key",
        "api": "openai-completions",
        "models": [
          {"id": "gpt-4o", "name": "GPT-4o", "maxTokens": 8192, "contextWindow": 128000}
        ]
      }
    }
  },
  "agents": {
    "defaults": {
      "model": {"primary": "my-openai/gpt-4o"},
      "systemPrompt": "You are a coding assistant."
    }
  }
}`

func TestLoadOpenClawJSON_Basic(t *testing.T) {
	path := writeTempJSON(t, sampleOpenClawJSON)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider.APIKey != "sk-live-key" {
		t.Errorf("want APIKey %q, got %q", "sk-live-key", cfg.Provider.APIKey)
	}
	if cfg.Provider.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("unexpected BaseURL %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.Model != "gpt-4o" {
		t.Errorf("want model gpt-4o, got %q", cfg.Provider.Model)
	}
	if cfg.Provider.MaxTokens != 8192 {
		t.Errorf("want MaxTokens 8192, got %d", cfg.Provider.MaxTokens)
	}
	if cfg.Provider.SystemPrompt != "You are a coding assistant." {
		t.Errorf("unexpected SystemPrompt %q", cfg.Provider.SystemPrompt)
	}
	// Defaults must still be applied.
	if cfg.MaxHistoryTurns != 20 {
		t.Errorf("want MaxHistoryTurns 20, got %d", cfg.MaxHistoryTurns)
	}
}

func TestLoadOpenClawJSON_EnvVarInterpolation(t *testing.T) {
	t.Setenv("TEST_API_KEY_1234", "sk-from-env")
	jsonContent := `{
  "models": {
    "providers": {
      "my-openai": {
        "baseUrl": "https://api.openai.com/v1",
        "apiKey": "${TEST_API_KEY_1234}",
        "api": "openai-completions",
        "models": [{"id": "gpt-4o", "maxTokens": 4096}]
      }
    }
  },
  "agents": {
    "defaults": {
      "model": {"primary": "my-openai/gpt-4o"}
    }
  }
}`
	path := writeTempJSON(t, jsonContent)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider.APIKey != "sk-from-env" {
		t.Errorf("want interpolated key %q, got %q", "sk-from-env", cfg.Provider.APIKey)
	}
}

func TestLoadOpenClawJSON_FallbackPicksFirstProvider(t *testing.T) {
	// No agents.defaults — loader should pick the first provider's first model.
	jsonContent := `{
  "models": {
    "providers": {
      "fallback-provider": {
        "baseUrl": "https://example.com",
        "apiKey": "sk-fb",
        "api": "openai-completions",
        "models": [{"id": "default-model", "maxTokens": 2048}]
      }
    }
  },
  "agents": {}
}`
	path := writeTempJSON(t, jsonContent)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Provider.Model != "default-model" {
		t.Errorf("want model %q, got %q", "default-model", cfg.Provider.Model)
	}
}

func TestLoadOpenClawJSON_MissingProvider(t *testing.T) {
	jsonContent := `{
  "models": {
    "providers": {}
  },
  "agents": {
    "defaults": {
      "model": {"primary": "missing/model"}
    }
  }
}`
	path := writeTempJSON(t, jsonContent)
	_, err := config.Load(path)
	if err == nil {
		t.Error("expected error when referenced provider does not exist")
	}
}
