// dirs/dirs_test.go
package dirs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XHao/claw-go/dirs"
)

func TestAgentsDir(t *testing.T) {
	t.Setenv("OPENCLAW_STATE_DIR", t.TempDir())
	got := dirs.AgentsDir()
	if !strings.HasSuffix(got, filepath.Join("agents")) {
		t.Errorf("AgentsDir() = %q, want suffix 'agents'", got)
	}
}

func TestAgentDir(t *testing.T) {
	t.Setenv("OPENCLAW_STATE_DIR", t.TempDir())
	got := dirs.AgentDir("lawyer")
	if !strings.HasSuffix(got, filepath.Join("agents", "lawyer")) {
		t.Errorf("AgentDir('lawyer') = %q, want suffix 'agents/lawyer'", got)
	}
}

func TestAgentMemoryDir(t *testing.T) {
	t.Setenv("OPENCLAW_STATE_DIR", t.TempDir())
	got := dirs.AgentMemoryDir("coder")
	want := filepath.Join(dirs.AgentDir("coder"), "memory")
	if got != want {
		t.Errorf("AgentMemoryDir('coder') = %q, want %q", got, want)
	}
}

func TestMkdirAllCreatesAgentsDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OPENCLAW_STATE_DIR", tmp)
	if err := dirs.MkdirAll(); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	if _, err := os.Stat(dirs.AgentsDir()); err != nil {
		t.Errorf("AgentsDir not created: %v", err)
	}
}
