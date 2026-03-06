// Package config loads and validates claw-go configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/XHao/claw-go/dirs"
)

// Config is the top-level configuration structure.
type Config struct {
	// SocketPath is the Unix Domain Socket path used by the daemon and client.
	// Defaults to ipc.DefaultSocketPath() when empty.
	SocketPath      string         `yaml:"socket_path"`
	MaxHistoryTurns int            `yaml:"max_history_turns"`
	Provider        ProviderConfig `yaml:"provider"`
	CLI             CLIConfig      `yaml:"cli"`
	Tools           ToolsConfig    `yaml:"tools"`
	Theme           ThemeConfig    `yaml:"theme"`
	Log             LogConfig      `yaml:"log"`
}

// LogConfig controls daemon logging behaviour.
type LogConfig struct {
	// DebugLLM enables detailed LLM request/response tracing written to DebugFile.
	// When true, every LLM call is logged in full — including all messages,
	// available tools, and the raw response — so you can audit prompt decisions.
	DebugLLM bool `yaml:"debug_llm"`
	// DebugFile is the path to the LLM debug trace file.
	// Defaults to ~/.claw/logs/llm_debug.log.
	DebugFile string `yaml:"debug_file"`
}

// ThemeConfig controls terminal colour output.
type ThemeConfig struct {
	// Preset selects a named colour scheme: "default", "dark", "minimal", "none".
	Preset string `yaml:"preset"`
	// Colors lets users override individual palette slots (ANSI SGR codes).
	Colors ThemeColors `yaml:"colors"`
}

// ThemeColors holds per-slot ANSI SGR parameter overrides (e.g. "1;36").
// An empty string means "keep the preset value for this slot".
type ThemeColors struct {
	Assistant string `yaml:"assistant"` // assistant label
	User      string `yaml:"user"`      // user prompt
	Dim       string `yaml:"dim"`       // secondary / muted text
	Success   string `yaml:"success"`   // success indicator
	Warn      string `yaml:"warn"`      // warnings, [busy]
	Error     string `yaml:"error"`     // error messages
	Bold      string `yaml:"bold"`      // emphasis
	Border    string `yaml:"border"`    // box-drawing characters
	Timestamp string `yaml:"timestamp"` // time display
	ToolName  string `yaml:"tool_name"` // tool name
}

// ToolsConfig controls the agentic tool-calling feature.
type ToolsConfig struct {
	// Enabled turns tool calling on/off globally.
	Enabled bool `yaml:"enabled"`
	// MaxIterations caps the number of tool-call/LLM-reply rounds per message.
	MaxIterations int `yaml:"max_iterations"`
	// Allowed restricts which tools the LLM may use. nil/empty = all built-in tools.
	Allowed []string `yaml:"allowed"`
	// BashTimeoutSeconds is the per-command timeout for bash tool calls.
	BashTimeoutSeconds int `yaml:"bash_timeout_seconds"`
}

// ProviderConfig describes the LLM backend.
type ProviderConfig struct {
	Type           string `yaml:"type"`
	BaseURL        string `yaml:"base_url"`
	APIKey         string `yaml:"api_key"`
	Model          string `yaml:"model"`
	SystemPrompt   string `yaml:"system_prompt"`
	MaxTokens      int    `yaml:"max_tokens"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

// CLIConfig holds settings for the interactive terminal client.
type CLIConfig struct {
	// Prompt is the readline prompt shown to the user.
	Prompt string `yaml:"prompt"`
	// HistoryFile persists command history across sessions; "" disables it.
	HistoryFile string `yaml:"history_file"`
	// ShellEnabled allows the user to run shell commands via the !<cmd> prefix.
	ShellEnabled bool `yaml:"shell_enabled"`
	// ShellTimeoutSeconds is the per-command timeout (0 = no limit).
	ShellTimeoutSeconds int `yaml:"shell_timeout_seconds"`
	// AllowedCommands restricts which shell commands may be executed.
	// An empty list means all commands are permitted.
	AllowedCommands []string `yaml:"allowed_commands"`
}

// Load reads and parses a config file. The format is selected by file
// extension: ".json" is treated as the official openclaw JSON format;
// ".yaml" / ".yml" (or any other extension) is treated as the Go-native YAML
// format. After parsing, environment-variable overrides and defaults are applied.
func Load(path string) (*Config, error) {
	var (
		cfg *Config
		err error
	)
	if strings.ToLower(filepath.Ext(path)) == ".json" {
		cfg, err = loadOpenClawJSON(path)
	} else {
		cfg, err = loadYAML(path)
	}
	if err != nil {
		return nil, err
	}
	applyDefaults(cfg)
	applyEnvOverrides(cfg)
	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("config: validation: %w", err)
	}
	return cfg, nil
}

// AutoLoad selects a config file automatically using the following priority:
//  1. Path explicitly supplied (non-empty).
//  2. OPENCLAW_CONFIG_PATH environment variable.
//  3. ~/.claw/config.yaml  (default data directory).
//  4. ./config.yaml        (local fallback for development).
func AutoLoad(path string) (*Config, error) {
	return Load(ResolveConfigPath(path))
}

// ResolveConfigPath returns the config file path that would be used by AutoLoad.
// It applies the same priority logic without reading the file.
func ResolveConfigPath(path string) string {
	if path != "" {
		return path
	}
	if envPath := os.Getenv("OPENCLAW_CONFIG_PATH"); envPath != "" {
		return expandHome(envPath)
	}
	// Default data directory config (e.g. ~/.claw/config.yaml).
	cfgPath := dirs.ConfigFile()
	if _, err := os.Stat(cfgPath); err == nil {
		return cfgPath
	}
	return "config.yaml"
}

// loadYAML parses a YAML config file.
func loadYAML(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse YAML: %w", err)
	}
	return &cfg, nil
}

// expandHome expands a leading ~ in a path.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func applyDefaults(cfg *Config) {
	if cfg.MaxHistoryTurns == 0 {
		cfg.MaxHistoryTurns = 20
	}
	if cfg.Provider.BaseURL == "" {
		cfg.Provider.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Provider.Model == "" {
		cfg.Provider.Model = "gpt-4o-mini"
	}
	if cfg.Provider.MaxTokens == 0 {
		cfg.Provider.MaxTokens = 4096
	}
	if cfg.Provider.TimeoutSeconds == 0 {
		cfg.Provider.TimeoutSeconds = 120
	}
	if cfg.Provider.SystemPrompt == "" {
		cfg.Provider.SystemPrompt = "You are a helpful assistant."
	}
	if cfg.CLI.Prompt == "" {
		cfg.CLI.Prompt = "You \u276f "
	}
	if cfg.CLI.HistoryFile == "" {
		cfg.CLI.HistoryFile = dirs.History()
	}
	if cfg.CLI.ShellTimeoutSeconds == 0 {
		cfg.CLI.ShellTimeoutSeconds = 300
	}
	// Tools defaults.
	if !cfg.Tools.Enabled && cfg.Tools.MaxIterations == 0 {
		// Default: tools disabled unless user opts in.
		cfg.Tools.Enabled = false
	}
	if cfg.Tools.MaxIterations == 0 {
		cfg.Tools.MaxIterations = 10
	}
	if cfg.Tools.BashTimeoutSeconds == 0 {
		cfg.Tools.BashTimeoutSeconds = 30
	}
	// Theme defaults.
	if cfg.Theme.Preset == "" {
		cfg.Theme.Preset = "default"
	}
	// Log defaults.
	if cfg.Log.DebugFile == "" {
		cfg.Log.DebugFile = dirs.DebugLogFile()
	}
}

func applyEnvOverrides(cfg *Config) {
	// OPENAI_API_KEY always wins — this lets users override any key loaded from
	// openclaw.json (e.g. an OAuth placeholder like "qwen-oauth") without having
	// to edit the shared config file.
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		cfg.Provider.APIKey = v
	}
}

func validate(cfg *Config) error {
	if cfg.Provider.Type == "" {
		cfg.Provider.Type = "openai"
	}
	if cfg.Provider.Type != "openai" {
		return fmt.Errorf("unsupported provider type %q", cfg.Provider.Type)
	}
	return nil
}

// ValidateServe performs additional validation required for daemon (serve) mode.
func ValidateServe(cfg *Config) error {
	if cfg.Provider.APIKey == "" {
		return fmt.Errorf("API key is not set — add provider.api_key to config or set OPENAI_API_KEY")
	}
	// Detect OAuth placeholder markers (e.g. "qwen-oauth", "anthropic-oauth").
	// The official openclaw handles these via browser login; claw-go does not
	// support OAuth flows and will receive a 401 if sent as a bearer token.
	if strings.HasSuffix(cfg.Provider.APIKey, "-oauth") {
		return fmt.Errorf(
			"API key %q looks like an OAuth placeholder used by the official openclaw client.\n"+
				"claw-go does not support OAuth flows — set OPENAI_API_KEY to a real API key",
			cfg.Provider.APIKey,
		)
	}
	return nil
}
