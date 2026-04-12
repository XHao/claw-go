// Package dirs provides canonical file-system paths for claw-go.
//
// All persistent data lives under a single root directory, configurable via
// OPENCLAW_STATE_DIR. Defaults to ~/.claw.
//
// Layout:
//
//	~/.claw/
//	├── config.yaml          go-native config
//	├── sessions/            one JSON file per named conversation
//	├── logs/                daemon log files
//	├── history              readline input history
//	└── data/
//	    ├── memory/
//	    │   └── {sessionKey}/   daily JSONL turn-summary files
//	    └── experiences/    per-topic Markdown knowledge files
package dirs

import (
	"os"
	"path/filepath"
)

// Data returns the root data directory for claw-go.
// Priority: $OPENCLAW_STATE_DIR then ~/.claw
func Data() string {
	if d := os.Getenv("OPENCLAW_STATE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "claw")
	}
	return filepath.Join(home, ".claw")
}

// Sessions returns the directory where per-conversation JSON files are stored.
func Sessions() string { return filepath.Join(Data(), "sessions") }

// Logs returns the directory for daemon log files.
func Logs() string { return filepath.Join(Data(), "logs") }

// LogFile returns the path to the main daemon log file.
func LogFile() string { return filepath.Join(Logs(), "claw-go.log") }

// DebugLogFile returns the path to the LLM debug trace file.
func DebugLogFile() string { return filepath.Join(Logs(), "llm_debug.log") }

// MetricsFile returns the path to the JSONL LLM metrics file.
func MetricsFile() string { return filepath.Join(Logs(), "llm_metrics.jsonl") }

// QuotaStateFile returns the path to the local LLM quota state file.
func QuotaStateFile() string { return filepath.Join(Logs(), "llm_quota_state.json") }

// History returns the path to the readline input-history file.
func History() string { return filepath.Join(Data(), "history") }

// ConfigFile returns the default go-native config file path inside the data
// directory. Used by install to place the config template.
func ConfigFile() string { return filepath.Join(Data(), "config.yaml") }

// SocketPath returns the default Unix Domain Socket path.
// XDG_RUNTIME_DIR is preferred (tmpfs, auto-cleaned); falls back to Data().
func SocketPath() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "claw-go.sock")
	}
	return filepath.Join(Data(), "claw-go.sock")
}

// MemoryDir returns the root directory for all session memory stores.
// Per-session short-term memory lives under {MemoryDir}/{sessionKey}/.
func MemoryDir() string { return filepath.Join(Data(), "data", "memory") }

// ExperiencesDir returns the directory for long-term per-topic experience files.
// Each file is a Markdown document named {topic}.md.
func ExperiencesDir() string { return filepath.Join(Data(), "data", "experiences") }

// PromptsDir returns the directory where user-defined prompt layer files live.
// Files are loaded at daemon startup to assemble the system prompt.
func PromptsDir() string { return filepath.Join(Data(), "prompts") }

// DynamicProfileFile returns the path to the agent-maintained user profile.
// Facts observed during conversations are appended here over time.
func DynamicProfileFile() string {
	return filepath.Join(Data(), "data", "user-profile-dynamic.md")
}

// WeixinTokenFile returns the default path for the WeChat bot_token persistence file.
func WeixinTokenFile() string { return filepath.Join(Data(), "weixin-token.json") }

// MkdirAll creates all necessary subdirectories under the data root.
// Should be called once at daemon startup.
func MkdirAll() error {
	for _, d := range []string{Sessions(), Logs(), MemoryDir(), ExperiencesDir(), PromptsDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
