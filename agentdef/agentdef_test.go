package agentdef_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/XHao/claw-go/agentdef"
)

func setupAgentDir(t *testing.T, name, personaMD, toolsYAML string) string {
	t.Helper()
	base := t.TempDir()
	agentDir := filepath.Join(base, name)
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if personaMD != "" {
		os.WriteFile(filepath.Join(agentDir, "persona.md"), []byte(personaMD), 0o600)
	}
	if toolsYAML != "" {
		os.WriteFile(filepath.Join(agentDir, "tools.yaml"), []byte(toolsYAML), 0o600)
	}
	return base
}

func TestRegistryLoad_SingleAgent(t *testing.T) {
	base := setupAgentDir(t, "lawyer", "你是法律顾问。", "extra_tools:\n  - legal_search\n")
	reg, err := agentdef.LoadRegistry(base)
	if err != nil {
		t.Fatalf("LoadRegistry error: %v", err)
	}
	def, ok := reg.Get("lawyer")
	if !ok {
		t.Fatal("expected 'lawyer' in registry")
	}
	if def.Persona != "你是法律顾问。" {
		t.Errorf("Persona = %q, want '你是法律顾问。'", def.Persona)
	}
	if len(def.ExtraTools) != 1 || def.ExtraTools[0] != "legal_search" {
		t.Errorf("ExtraTools = %v, want [legal_search]", def.ExtraTools)
	}
}

func TestRegistryLoad_EmptyDir(t *testing.T) {
	base := t.TempDir()
	reg, err := agentdef.LoadRegistry(base)
	if err != nil {
		t.Fatalf("LoadRegistry error: %v", err)
	}
	if len(reg.List()) != 0 {
		t.Errorf("expected empty registry, got %v", reg.List())
	}
}

func TestRegistryLoad_NoPersonaFile(t *testing.T) {
	base := setupAgentDir(t, "default", "", "")
	reg, err := agentdef.LoadRegistry(base)
	if err != nil {
		t.Fatalf("LoadRegistry error: %v", err)
	}
	def, ok := reg.Get("default")
	if !ok {
		t.Fatal("expected 'default' in registry")
	}
	if def.Persona != "" {
		t.Errorf("expected empty Persona, got %q", def.Persona)
	}
}

func TestRegistryList(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"default", "lawyer", "coder"} {
		os.MkdirAll(filepath.Join(base, name), 0o700)
	}
	reg, err := agentdef.LoadRegistry(base)
	if err != nil {
		t.Fatalf("LoadRegistry error: %v", err)
	}
	names := reg.List()
	if len(names) != 3 {
		t.Errorf("List() = %v, want 3 agents", names)
	}
}

func TestRegistryCreate(t *testing.T) {
	base := t.TempDir()
	reg, _ := agentdef.LoadRegistry(base)
	if err := reg.Create(base, "newagent"); err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "newagent", "persona.md")); err != nil {
		t.Errorf("persona.md not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "newagent", "memory")); err != nil {
		t.Errorf("memory dir not created: %v", err)
	}
}

func TestRegistry_ConcurrentGetAndCreate(t *testing.T) {
	base := t.TempDir()
	os.MkdirAll(filepath.Join(base, "default"), 0o700)
	reg, err := agentdef.LoadRegistry(base)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.Get("default")
			reg.List()
		}()
	}
	for i := 0; i < 5; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = reg.Create(base, fmt.Sprintf("agent%d", i))
		}()
	}
	wg.Wait()
}
