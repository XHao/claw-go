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

// DingTalkConfig holds settings for the DingTalk (钉钉) bot channel.
// The channel uses DingTalk's Stream API (WebSocket long connection) — no public
// HTTP server is required. The bot connects outbound to DingTalk's gateway and
// receives messages in real time.
//
// Credentials: find ClientID (AppKey) and ClientSecret (AppSecret) in the
// DingTalk developer console under your application → "AppKey and AppSecret".
type DingTalkConfig struct {
	// Enabled turns the DingTalk channel on/off.
	Enabled bool `yaml:"enabled"`
	// ClientID is the DingTalk AppKey.
	ClientID string `yaml:"client_id"`
	// ClientSecret is the DingTalk AppSecret.
	ClientSecret string `yaml:"client_secret"`
}

// WeixinConfig holds settings for the WeChat iLink Bot channel.
// The channel uses WeChat's iLink Bot API with QR-code login — no public
// HTTP server is required. On first start, a QR code is printed to the
// terminal; after the user scans it, the bot_token is saved to TokenFile
// and reused on subsequent starts.
//
// Credentials: obtained automatically via QR-code scan. No manual setup needed.
type WeixinConfig struct {
	// Enabled turns the WeChat channel on/off.
	Enabled bool `yaml:"enabled"`
	// TokenFile is the path where the bot_token is persisted after login.
	// Defaults to ~/.claw/weixin-token.json when empty.
	TokenFile string `yaml:"token_file"`
}

// Config is the top-level configuration structure.
type Config struct {
	// SocketPath is the Unix Domain Socket path used by the daemon and client.
	// Defaults to ipc.DefaultSocketPath() when empty.
	SocketPath           string                   `yaml:"socket_path"`
	MaxHistoryTurns      int                      `yaml:"max_history_turns"`
	MaxHistoryTokens     int                      `yaml:"max_history_tokens"`
	RecentRawTurns       int                      `yaml:"recent_raw_turns"`
	HistoryCharsPerToken float64                  `yaml:"history_chars_per_token"`
	HistoryBudgetScale   HistoryBudgetScaleConfig `yaml:"history_budget_scale"`
	Provider             ProviderConfig           `yaml:"provider"`
	// Models is a reusable provider catalog keyed by logical model name.
	// Used by RoutingPolicy to separate model definitions from routing rules.
	Models map[string]ProviderConfig `yaml:"models"`
	// PrimaryModel is the required primary model name in Models.
	// It is used when routing_policy is disabled and as runtime fallback on errors.
	PrimaryModel string `yaml:"primary_model"`
	// RoutingPolicy maps each logical tier to a model name in Models.
	RoutingPolicy RoutingPolicyConfig `yaml:"routing_policy"`
	CLI           CLIConfig           `yaml:"cli"`
	DingTalk      DingTalkConfig      `yaml:"dingtalk"`
	Weixin        WeixinConfig        `yaml:"weixin"`
	Tools         ToolsConfig         `yaml:"tools"`
	Search        SearchConfig        `yaml:"search"`
	Theme         ThemeConfig         `yaml:"theme"`
	Log           LogConfig           `yaml:"log"`
}

// HistoryBudgetScaleConfig configures hint-specific multipliers applied when
// building the final history view for router/task/summary/thinking calls.
type HistoryBudgetScaleConfig struct {
	Router   float64 `yaml:"router"`
	Task     float64 `yaml:"task"`
	Summary  float64 `yaml:"summary"`
	Thinking float64 `yaml:"thinking"`
}

// RoutingPolicyConfig maps tiers to named model entries from Config.Models.
// TaskModel is required when the policy is enabled.
type RoutingPolicyConfig struct {
	RoutingModel     string   `yaml:"routing_model"`
	TaskModel        string   `yaml:"task_model"`
	SummaryModel     string   `yaml:"summary_model"`
	ThinkingModel    string   `yaml:"thinking_model"`
	ThinkingKeywords []string `yaml:"thinking_keywords"`
}

// IsEnabled reports whether routing policy is configured.
func (r RoutingPolicyConfig) IsEnabled() bool {
	return strings.TrimSpace(r.TaskModel) != ""
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
	// MetricsFile is the path to the JSONL metrics file.
	// Each line is a JSON object with hint, token counts, latency, etc.
	// Defaults to ~/.claw/logs/llm_metrics.jsonl when MetricsEnabled is true.
	MetricsEnabled bool   `yaml:"metrics_enabled"`
	MetricsFile    string `yaml:"metrics_file"`
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
	// Allowed restricts which built-in tools the LLM may use. nil/empty = all built-in tools.
	Allowed []string `yaml:"allowed"`
	// BashTimeoutSeconds is the per-command timeout for bash tool calls.
	BashTimeoutSeconds int `yaml:"bash_timeout_seconds"`
	// BashAllowedCommands restricts which shell commands the bash tool may execute.
	// nil/empty = all commands are permitted.
	BashAllowedCommands []string `yaml:"bash_allowed_commands"`
}

// SearchConfig configures the web search skill backed by Tavily.
type SearchConfig struct {
	// TavilyAPIKey is the Tavily REST API key (https://app.tavily.com).
	// When empty, the web_search skill is not registered.
	TavilyAPIKey string `yaml:"tavily_api_key"`
	// MaxResults controls how many results are returned (default 5).
	MaxResults int `yaml:"max_results"`
	// TimeoutSeconds is the per-request HTTP timeout (default 15).
	TimeoutSeconds int `yaml:"timeout_seconds"`
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

// ProviderConfig describes the LLM backend.
type ProviderConfig struct {
	Type           string `yaml:"type"`
	BaseURL        string `yaml:"base_url"`
	APIKey         string `yaml:"api_key"`
	Model          string `yaml:"model"`
	SystemPrompt   string `yaml:"system_prompt"`
	MaxTokens      int    `yaml:"max_tokens"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	// ThinkingBudget enables extended thinking (Anthropic-style) and sets the
	// budget_tokens value sent in the request body.  0 means thinking is disabled.
	// When set, the API receives {"thinking":{"type":"enabled","budget_tokens":N}}.
	// Must be less than MaxTokens.
	ThinkingBudget int `yaml:"thinking_budget"`
	// Stream enables SSE streaming for real-time token output.
	// Defaults to true when omitted.  Set to false for providers that don't
	// support the OpenAI streaming format (e.g. some Ollama versions).
	Stream *bool `yaml:"stream"`
	// Headers are extra HTTP headers sent with every request to this provider.
	// Useful for internal proxies that require custom authentication headers
	// (e.g. X-Working-Dir for mcli).
	Headers map[string]string `yaml:"headers"`
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
// Environment variables in the form $VAR or ${VAR} are expanded before parsing.
func loadYAML(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	data = []byte(os.ExpandEnv(string(data)))
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
	if cfg.RecentRawTurns == 0 {
		if cfg.MaxHistoryTurns < 4 {
			cfg.RecentRawTurns = cfg.MaxHistoryTurns
		} else {
			cfg.RecentRawTurns = 4
		}
	}
	if cfg.HistoryBudgetScale.Router == 0 {
		cfg.HistoryBudgetScale.Router = 0.35
	}
	if cfg.HistoryBudgetScale.Task == 0 {
		cfg.HistoryBudgetScale.Task = 1.0
	}
	if cfg.HistoryBudgetScale.Summary == 0 {
		cfg.HistoryBudgetScale.Summary = 0.85
	}
	if cfg.HistoryBudgetScale.Thinking == 0 {
		cfg.HistoryBudgetScale.Thinking = 1.5
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
	for name, pc := range cfg.Models {
		if pc.BaseURL == "" {
			pc.BaseURL = "https://api.openai.com/v1"
		}
		if pc.MaxTokens == 0 {
			pc.MaxTokens = 4096
		}
		if pc.TimeoutSeconds == 0 {
			pc.TimeoutSeconds = 120
		}
		cfg.Models[name] = pc
	}
	if cfg.Provider.SystemPrompt == "" && strings.TrimSpace(cfg.PrimaryModel) != "" {
		if pc, ok := cfg.Models[cfg.PrimaryModel]; ok && strings.TrimSpace(pc.SystemPrompt) != "" {
			cfg.Provider.SystemPrompt = pc.SystemPrompt
		}
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
		cfg.Tools.MaxIterations = 20
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
	if cfg.Log.MetricsFile == "" {
		cfg.Log.MetricsFile = dirs.MetricsFile()
	}
}

func applyEnvOverrides(cfg *Config) {
	// OPENAI_API_KEY overrides api_key for all models. When ANTHROPIC_API_KEY is
	// also set, it takes precedence for anthropic-type models (applied below).
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		cfg.Provider.APIKey = v
		for name, pc := range cfg.Models {
			pc.APIKey = v
			cfg.Models[name] = pc
		}
	}
	// ANTHROPIC_API_KEY overrides api_key for all anthropic-type models.
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		for name, pc := range cfg.Models {
			if pc.Type == "anthropic" {
				pc.APIKey = v
				cfg.Models[name] = pc
			}
		}
		if cfg.Provider.Type == "anthropic" {
			cfg.Provider.APIKey = v
		}
	}
}

func validate(cfg *Config) error {
	if cfg.MaxHistoryTurns < 0 {
		return fmt.Errorf("max_history_turns must be >= 0")
	}
	if cfg.MaxHistoryTokens < 0 {
		return fmt.Errorf("max_history_tokens must be >= 0")
	}
	if cfg.RecentRawTurns < 0 {
		return fmt.Errorf("recent_raw_turns must be >= 0")
	}
	if cfg.HistoryCharsPerToken < 0 {
		return fmt.Errorf("history_chars_per_token must be >= 0")
	}
	if cfg.HistoryCharsPerToken > 0 && cfg.HistoryCharsPerToken < 1 {
		return fmt.Errorf("history_chars_per_token must be >= 1 when set")
	}
	for name, value := range map[string]float64{
		"history_budget_scale.router":   cfg.HistoryBudgetScale.Router,
		"history_budget_scale.task":     cfg.HistoryBudgetScale.Task,
		"history_budget_scale.summary":  cfg.HistoryBudgetScale.Summary,
		"history_budget_scale.thinking": cfg.HistoryBudgetScale.Thinking,
	} {
		if value < 0 {
			return fmt.Errorf("%s must be >= 0", name)
		}
		if value > 0 && value < 0.1 {
			return fmt.Errorf("%s must be >= 0.1 when set", name)
		}
	}
	return nil
}

// ValidateServe performs additional validation required for daemon (serve) mode.
func ValidateServe(cfg *Config) error {
	if len(cfg.Models) == 0 {
		return fmt.Errorf("models catalog is empty")
	}
	if strings.TrimSpace(cfg.PrimaryModel) == "" {
		return fmt.Errorf("primary_model is required")
	}
	defaultPC, ok := cfg.Models[cfg.PrimaryModel]
	if !ok {
		return fmt.Errorf("primary_model references unknown model %q", cfg.PrimaryModel)
	}
	if err := validateProviderConfig("models."+cfg.PrimaryModel, &defaultPC); err != nil {
		return err
	}

	if cfg.RoutingPolicy.IsEnabled() {
		mustGet := func(tier, name string) (*ProviderConfig, error) {
			pc, ok := cfg.Models[name]
			if !ok {
				return nil, fmt.Errorf("routing_policy.%s_model references unknown model %q", tier, name)
			}
			return &pc, nil
		}
		taskPC, err := mustGet("task", cfg.RoutingPolicy.TaskModel)
		if err != nil {
			return err
		}
		if err := validateProviderConfig("models."+cfg.RoutingPolicy.TaskModel, taskPC); err != nil {
			return err
		}
		for tier, modelName := range map[string]string{
			"routing":  cfg.RoutingPolicy.RoutingModel,
			"summary":  cfg.RoutingPolicy.SummaryModel,
			"thinking": cfg.RoutingPolicy.ThinkingModel,
		} {
			if strings.TrimSpace(modelName) == "" {
				continue
			}
			pc, err := mustGet(tier, modelName)
			if err != nil {
				return err
			}
			if err := validateProviderConfig("models."+modelName, pc); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

// validateProviderConfig checks that a ProviderConfig has the minimum required fields.
func validateProviderConfig(name string, pc *ProviderConfig) error {
	if pc.APIKey == "" {
		return fmt.Errorf("%s: api_key is not set", name)
	}
	if strings.HasSuffix(pc.APIKey, "-oauth") {
		return fmt.Errorf("%s: api_key %q looks like an OAuth placeholder; set a real API key", name, pc.APIKey)
	}
	return nil
}
