package agentdef

import (
	"encoding/json"
	"errors"
	"os"
)

// AgentState holds the runtime-mutable agent configuration persisted to
// agent-state.json. It is separate from config.yaml so the default agent
// can be changed without editing the config file.
type AgentState struct {
	Default string `json:"default"`
}

// LoadState reads agent-state.json at path.
// Returns a zero-value AgentState (no error) when the file does not exist.
func LoadState(path string) (AgentState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return AgentState{}, nil
	}
	if err != nil {
		return AgentState{}, err
	}
	var s AgentState
	if err := json.Unmarshal(data, &s); err != nil {
		return AgentState{}, err
	}
	return s, nil
}

// SaveState atomically writes s to path using a temp-file + rename pattern.
func SaveState(path string, s AgentState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
