// dirs/dirs_test.go
package dirs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/XHao/claw-go/dirs"
)

func TestExperiencesDir(t *testing.T) {
	tmpdir := t.TempDir()
	t.Setenv("OPENCLAW_STATE_DIR", tmpdir)
	got := dirs.ExperiencesDir()
	want := filepath.Join(tmpdir, "experiences")
	if got != want {
		t.Errorf("ExperiencesDir() = %q, want %q", got, want)
	}
}

func TestProceduresDir(t *testing.T) {
	tmpdir := t.TempDir()
	t.Setenv("OPENCLAW_STATE_DIR", tmpdir)
	got := dirs.ProceduresDir()
	want := filepath.Join(tmpdir, "procedures")
	if got != want {
		t.Errorf("ProceduresDir() = %q, want %q", got, want)
	}
}

func TestMkdirAll(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("OPENCLAW_STATE_DIR", tmp)
	if err := dirs.MkdirAll(); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}
	// Verify that expected directories are created
	if _, err := os.Stat(dirs.Sessions()); err != nil {
		t.Errorf("Sessions directory not created: %v", err)
	}
	if _, err := os.Stat(dirs.Logs()); err != nil {
		t.Errorf("Logs directory not created: %v", err)
	}
	if _, err := os.Stat(dirs.MemoryDir()); err != nil {
		t.Errorf("MemoryDir not created: %v", err)
	}
	if _, err := os.Stat(dirs.PromptsDir()); err != nil {
		t.Errorf("PromptsDir not created: %v", err)
	}
	if _, err := os.Stat(dirs.ExperiencesDir()); err != nil {
		t.Errorf("ExperiencesDir not created: %v", err)
	}
	if _, err := os.Stat(dirs.ProceduresDir()); err != nil {
		t.Errorf("ProceduresDir not created: %v", err)
	}
}
