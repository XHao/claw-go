package agent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/XHao/claw-go/agentdef"
)

func TestSingleAgentMode_EmptyRegistry(t *testing.T) {
	// Empty registry → IsMultiAgent() == false.
	tmp := t.TempDir()
	reg, err := agentdef.LoadRegistry(tmp)
	if err != nil {
		t.Fatalf("LoadRegistry error: %v", err)
	}
	if reg.IsMultiAgent() {
		t.Fatal("empty registry should not be multi-agent")
	}
}

func TestSingleAgentMode_OneAgent(t *testing.T) {
	// One agent only → IsMultiAgent() == false.
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "default"), 0o700)
	reg, err := agentdef.LoadRegistry(tmp)
	if err != nil {
		t.Fatalf("LoadRegistry error: %v", err)
	}
	if reg.IsMultiAgent() {
		t.Fatal("single agent registry should not be multi-agent")
	}
}

func TestMultiAgentMode_TwoAgents(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"default", "lawyer"} {
		dir := filepath.Join(tmp, name)
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, "persona.md"), []byte("persona: "+name), 0o600)
	}
	reg, err := agentdef.LoadRegistry(tmp)
	if err != nil {
		t.Fatalf("LoadRegistry error: %v", err)
	}
	if !reg.IsMultiAgent() {
		t.Fatal("expected multi-agent mode with 2 agents")
	}
	if _, ok := reg.Get("lawyer"); !ok {
		t.Error("lawyer agent not found")
	}
	if _, ok := reg.Get("default"); !ok {
		t.Error("default agent not found")
	}
}

func TestMultiAgentMode_PersonaLoaded(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"default", "coder"} {
		dir := filepath.Join(tmp, name)
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, "persona.md"), []byte("You are "+name+"."), 0o600)
	}
	reg, err := agentdef.LoadRegistry(tmp)
	if err != nil {
		t.Fatalf("LoadRegistry error: %v", err)
	}
	def, ok := reg.Get("coder")
	if !ok {
		t.Fatal("coder agent not found")
	}
	if def.Persona != "You are coder." {
		t.Errorf("Persona = %q, want 'You are coder.'", def.Persona)
	}
}
