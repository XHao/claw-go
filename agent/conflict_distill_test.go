package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
	"log/slog"
)

// conflictProvider: first Complete call returns eval response, subsequent calls return distill response.
type conflictProvider struct {
	evalResp    string
	distillResp string
	mu          sync.Mutex
	callCount   int
}

func (p *conflictProvider) Complete(_ context.Context, _ []provider.Message) (string, error) {
	p.mu.Lock()
	p.callCount++
	n := p.callCount
	p.mu.Unlock()
	if n == 1 {
		return p.evalResp, nil
	}
	return p.distillResp, nil
}

func (p *conflictProvider) CompleteWithTools(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (provider.CompleteResult, error) {
	return provider.CompleteResult{Content: "ok", StopReason: "stop"}, nil
}

func TestSaveTurnMemory_ConflictSummaryTriggersFullDistill(t *testing.T) {
	memDir := t.TempDir()
	expDir := t.TempDir()

	mgr := memory.NewManager(memDir)
	expStore := knowledge.NewExperienceStore(expDir)

	// Pre-populate memory with a relevant turn so Distill has content to work with.
	sess := mgr.ForSession("s1")
	_ = sess.SaveTurn(memory.TurnSummary{
		N: 1, At: time.Now().Add(-time.Hour),
		User:  "docker 网络用 bridge 模式",
		Reply: "bridge 模式适合单机",
	})

	// EvalTurn returns conflict signal; Distill map/reduce returns merged content.
	cp := &conflictProvider{
		evalResp:    `{"valuable":true,"topic":"docker","summary":"更新：overlay 模式替代 bridge 模式用于多主机"}`,
		distillResp: "overlay 模式（[已更新]：比 bridge 更适合多主机）",
	}
	distiller := knowledge.NewDistiller(cp, mgr, expStore)

	sessStore := session.NewStore(0, "", "")
	a := New(&stubProvider{}, sessStore, slog.Default())
	a.SetMemory(mgr)
	a.SetDistiller(distiller)

	// Call saveTurnMemory directly (white-box).
	a.saveTurnMemory(slog.Default(), "s1", 2,
		"docker overlay 网络", "overlay 更适合多主机", nil, 1, false, distiller)

	// Give async goroutine time to complete.
	time.Sleep(300 * time.Millisecond)

	content, err := expStore.Load("docker")
	if err != nil {
		t.Fatal(err)
	}
	// Full Distill (Map→Reduce) writes a header with "> 最后更新:" from Distill().
	// Simple append writes "# docker\n\n更新：..." without that header.
	// We verify the Reduce output was used by checking for the distillResp marker.
	if !strings.Contains(content, "[已更新]") {
		t.Errorf("expected full distill Reduce output containing '[已更新]', got: %s", content)
	}
}
