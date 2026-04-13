package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/session"
	"log/slog"
)

func TestPersistProcedureLayer_WritesPromptFile(t *testing.T) {
	procDir := t.TempDir()
	promptsDir := t.TempDir()

	procStore := knowledge.NewProcedureStore(procDir)
	_ = procStore.Save("debug-golang", knowledge.ProcedureFile{
		Name:     "Golang 调试流程",
		Tags:     []string{"debug"},
		Priority: 10,
		Body:     "遇到 panic 先跑 go test -race",
	})

	sessStore := session.NewStore(0, "", "")
	a := New(&stubProvider{}, sessStore, slog.Default())
	a.SetProcedureStore(procStore)
	a.SetReloadFunc(func() (string, error) {
		return "reloaded", nil
	})

	a.persistProcedureLayer(promptsDir)

	path := filepath.Join(promptsDir, "20-procedures.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected file at %s, got error: %v", path, err)
	}
	if !strings.Contains(string(data), "Golang 调试流程") {
		t.Errorf("file missing procedure name: %s", string(data))
	}
	if !strings.Contains(string(data), "go test -race") {
		t.Errorf("file missing procedure body: %s", string(data))
	}
}

func TestPersistProcedureLayer_RemovesFileWhenEmpty(t *testing.T) {
	procDir := t.TempDir()
	promptsDir := t.TempDir()

	procStore := knowledge.NewProcedureStore(procDir)
	// Empty store — no procedures.

	sessStore := session.NewStore(0, "", "")
	a := New(&stubProvider{}, sessStore, slog.Default())
	a.SetProcedureStore(procStore)
	a.SetReloadFunc(func() (string, error) { return "reloaded", nil })

	path := filepath.Join(promptsDir, "20-procedures.md")
	// Pre-create the file to verify it gets removed.
	_ = os.WriteFile(path, []byte("old content"), 0o600)

	a.persistProcedureLayer(promptsDir)

	_, err := os.Stat(path)
	if !os.IsNotExist(err) {
		t.Error("expected file to be removed when store is empty")
	}
}
