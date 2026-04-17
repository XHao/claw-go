package tool_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/tool"
)

func TestRecallMemory_ReturnsRelevantTurns(t *testing.T) {
	dir := t.TempDir()
	mgr := memory.NewManager(dir)
	sess := mgr.ForSession("s1")
	_ = sess.SaveTurn(memory.TurnSummary{
		N: 1, At: time.Now(),
		User:  "docker compose 网络配置",
		Reply: "使用 bridge 模式可以让容器互通",
	})

	runner := &tool.LocalRunner{}
	tool.RegisterRecallMemory(runner, mgr)

	argsJSON := `{"query":"docker 网络"}`
	out, isErr := runner.Run(context.Background(), "recall_memory", argsJSON, tool.RunContext{}, nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	turns, ok := result["turns"].([]any)
	if !ok || len(turns) == 0 {
		t.Errorf("want at least 1 turn in result, got: %s", out)
		return
	}
	first, ok := turns[0].(map[string]any)
	if !ok {
		t.Fatalf("turn[0] is not a map: %T", turns[0])
	}
	if first["user"] != "docker compose 网络配置" {
		t.Errorf("unexpected turn user: %v", first["user"])
	}
}

func TestRecallMemory_EmptyQueryReturnsError(t *testing.T) {
	dir := t.TempDir()
	mgr := memory.NewManager(dir)
	runner := &tool.LocalRunner{}
	tool.RegisterRecallMemory(runner, mgr)

	out, isErr := runner.Run(context.Background(), "recall_memory", `{"query":""}`, tool.RunContext{}, nil)
	if !isErr {
		t.Errorf("want error for empty query, got: %s", out)
	}
}

func TestRecallMemory_NoHistoryReturnsEmptyTurns(t *testing.T) {
	dir := t.TempDir()
	mgr := memory.NewManager(dir)
	runner := &tool.LocalRunner{}
	tool.RegisterRecallMemory(runner, mgr)

	out, isErr := runner.Run(context.Background(), "recall_memory", `{"query":"docker"}`, tool.RunContext{}, nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if !strings.Contains(out, "no relevant history found") {
		t.Errorf("want 'no relevant history found' note, got: %s", out)
	}
}

func makeStoreGetter(exp *knowledge.ExperienceStore, proc *knowledge.ProcedureStore) func() (*knowledge.ExperienceStore, *knowledge.ProcedureStore) {
	return func() (*knowledge.ExperienceStore, *knowledge.ProcedureStore) { return exp, proc }
}

func TestSaveMemory_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	expStore := knowledge.NewExperienceStore(dir)
	runner := &tool.LocalRunner{}
	tool.RegisterSaveMemory(runner, makeStoreGetter(expStore, nil), nil)

	argsJSON := `{"topic":"golang","content":"goroutine 泄漏可用 pprof /debug/pprof/goroutine 排查","type":"knowledge"}`
	out, isErr := runner.Run(context.Background(), "save_memory", argsJSON, tool.RunContext{}, nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	content, err := expStore.Load("golang")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "pprof") {
		t.Errorf("saved content missing expected text, got: %s", content)
	}
}

func TestSaveMemory_AppendsToExistingFile(t *testing.T) {
	dir := t.TempDir()
	expStore := knowledge.NewExperienceStore(dir)
	_ = expStore.Save("golang", "# golang\n\n旧内容")
	runner := &tool.LocalRunner{}
	tool.RegisterSaveMemory(runner, makeStoreGetter(expStore, nil), nil)

	argsJSON := `{"topic":"golang","content":"新内容：channel 方向类型提升安全性"}`
	_, isErr := runner.Run(context.Background(), "save_memory", argsJSON, tool.RunContext{}, nil)
	if isErr {
		t.Fatal("unexpected error")
	}
	content, _ := expStore.Load("golang")
	if !strings.Contains(content, "旧内容") {
		t.Error("old content should be preserved")
	}
	if !strings.Contains(content, "新内容") {
		t.Error("new content should be appended")
	}
}

func TestSaveMemory_EmptyTopicReturnsError(t *testing.T) {
	dir := t.TempDir()
	expStore := knowledge.NewExperienceStore(dir)
	runner := &tool.LocalRunner{}
	tool.RegisterSaveMemory(runner, makeStoreGetter(expStore, nil), nil)

	out, isErr := runner.Run(context.Background(), "save_memory", `{"topic":"","content":"something"}`, tool.RunContext{}, nil)
	if !isErr {
		t.Errorf("want error for empty topic, got: %s", out)
	}
}

func TestSaveMemory_EmptyContentReturnsError(t *testing.T) {
	dir := t.TempDir()
	expStore := knowledge.NewExperienceStore(dir)
	runner := &tool.LocalRunner{}
	tool.RegisterSaveMemory(runner, makeStoreGetter(expStore, nil), nil)

	out, isErr := runner.Run(context.Background(), "save_memory", `{"topic":"golang","content":""}`, tool.RunContext{}, nil)
	if !isErr {
		t.Errorf("want error for empty content, got: %s", out)
	}
}

func TestSaveMemory_ProcedureWritesToProcedureStore(t *testing.T) {
	expDir := t.TempDir()
	procDir := t.TempDir()
	expStore := knowledge.NewExperienceStore(expDir)
	procStore := knowledge.NewProcedureStore(procDir)
	runner := &tool.LocalRunner{}
	tool.RegisterSaveMemory(runner, makeStoreGetter(expStore, procStore), nil)

	argsJSON := `{"topic":"debug-golang","content":"遇到 panic 先跑 go test -race","type":"procedure"}`
	out, isErr := runner.Run(context.Background(), "save_memory", argsJSON, tool.RunContext{}, nil)
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}

	procs, err := procStore.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 1 {
		t.Fatalf("want 1 procedure, got %d", len(procs))
	}
	if !strings.Contains(procs[0].Body, "go test -race") {
		t.Errorf("procedure body missing content: %s", procs[0].Body)
	}

	// Experience store should be untouched.
	expContent, _ := expStore.Load("debug-golang")
	if expContent != "" {
		t.Error("experience store should not be written for type=procedure")
	}
}

func TestSaveMemory_ProcedureCallsCallback(t *testing.T) {
	expDir := t.TempDir()
	procDir := t.TempDir()
	expStore := knowledge.NewExperienceStore(expDir)
	procStore := knowledge.NewProcedureStore(procDir)
	runner := &tool.LocalRunner{}

	called := make(chan struct{}, 1)
	tool.RegisterSaveMemory(runner, makeStoreGetter(expStore, procStore), func() {
		called <- struct{}{}
	})

	argsJSON := `{"topic":"deploy","content":"先 build 再 push","type":"procedure"}`
	_, isErr := runner.Run(context.Background(), "save_memory", argsJSON, tool.RunContext{}, nil)
	if isErr {
		t.Fatal("unexpected error")
	}

	select {
	case <-called:
		// callback was invoked
	case <-time.After(200 * time.Millisecond):
		t.Error("onProcedureSaved callback was not called")
	}
}
