package agentdef_test

import (
	"path/filepath"
	"testing"

	"github.com/XHao/claw-go/agentdef"
)

func TestLoadState_Missing(t *testing.T) {
	dir := t.TempDir()
	state, err := agentdef.LoadState(filepath.Join(dir, "agent-state.json"))
	if err != nil {
		t.Fatalf("LoadState on missing file should not error, got: %v", err)
	}
	if state.Default != "" {
		t.Errorf("Default = %q, want empty string", state.Default)
	}
}

func TestSaveAndLoadState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")

	if err := agentdef.SaveState(path, agentdef.AgentState{Default: "finance"}); err != nil {
		t.Fatalf("SaveState error: %v", err)
	}

	state, err := agentdef.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState error: %v", err)
	}
	if state.Default != "finance" {
		t.Errorf("Default = %q, want %q", state.Default, "finance")
	}
}

func TestSaveState_Atomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")

	agentdef.SaveState(path, agentdef.AgentState{Default: "code"})
	agentdef.SaveState(path, agentdef.AgentState{Default: "maths"})

	state, _ := agentdef.LoadState(path)
	if state.Default != "maths" {
		t.Errorf("Default = %q, want %q", state.Default, "maths")
	}
}
