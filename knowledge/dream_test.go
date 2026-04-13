package knowledge_test

import (
	"context"
	"testing"
	"time"

	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
)

// dreamProvider: Complete returns a fixed map/reduce response.
type dreamProvider struct {
	response string
}

func (p *dreamProvider) Complete(_ context.Context, _ []provider.Message) (string, error) {
	return p.response, nil
}

func (p *dreamProvider) CompleteWithTools(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (provider.CompleteResult, error) {
	return provider.CompleteResult{Content: "ok"}, nil
}

func TestDreamCycle_Run_DistillsHighFrequencyTopics(t *testing.T) {
	memDir := t.TempDir()
	expDir := t.TempDir()

	mgr := memory.NewManager(memDir)
	expStore := knowledge.NewExperienceStore(expDir)

	// Seed 4 turns mentioning "docker" (above default minFreq=3).
	sess := mgr.ForSession("s1")
	for i := 1; i <= 4; i++ {
		_ = sess.SaveTurn(memory.TurnSummary{
			N:    i,
			At:   time.Now().Add(-time.Duration(i) * time.Hour),
			User: "docker 容器网络配置问题",
		})
	}
	// Seed 1 turn mentioning "golang" (below minFreq).
	_ = sess.SaveTurn(memory.TurnSummary{
		N:    5,
		At:   time.Now().Add(-time.Hour),
		User: "golang goroutine 用法",
	})

	dp := &dreamProvider{response: "docker 网络经验要点"}
	distiller := knowledge.NewDistiller(dp, mgr, expStore)
	dream := knowledge.NewDreamCycle(distiller, mgr, expStore, 3, 7)

	distilled, err := dream.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// "docker" should be distilled (freq >= minFreq=3).
	found := false
	for _, topic := range distilled {
		if topic == "docker" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'docker' to be distilled, got topics: %v", distilled)
	}

	// "golang" should NOT be distilled (freq < minFreq=3).
	for _, topic := range distilled {
		if topic == "golang" {
			t.Errorf("'golang' should not be distilled (freq too low)")
		}
	}
}

func TestDreamCycle_Run_SkipsRecentlyDistilledTopics(t *testing.T) {
	memDir := t.TempDir()
	expDir := t.TempDir()

	mgr := memory.NewManager(memDir)
	expStore := knowledge.NewExperienceStore(expDir)

	// Pre-populate experience file (simulating recent distillation).
	_ = expStore.Save("docker", "# docker\n\n已有经验")

	sess := mgr.ForSession("s1")
	for i := 1; i <= 4; i++ {
		_ = sess.SaveTurn(memory.TurnSummary{
			N:    i,
			At:   time.Now().Add(-time.Duration(i) * time.Hour),
			User: "docker 网络问题",
		})
	}

	dp := &dreamProvider{response: "新经验"}
	distiller := knowledge.NewDistiller(dp, mgr, expStore)
	dream := knowledge.NewDreamCycle(distiller, mgr, expStore, 3, 7)

	distilled, err := dream.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// "docker" should be skipped because the experience file was just updated.
	for _, topic := range distilled {
		if topic == "docker" {
			t.Errorf("'docker' should be skipped (recently distilled)")
		}
	}
}

func TestDreamCycle_Run_RespectsLookbackWindow(t *testing.T) {
	memDir := t.TempDir()
	expDir := t.TempDir()

	mgr := memory.NewManager(memDir)
	expStore := knowledge.NewExperienceStore(expDir)

	sess := mgr.ForSession("s1")
	// 4 turns about "docker", but all older than lookbackDays=3.
	for i := 1; i <= 4; i++ {
		_ = sess.SaveTurn(memory.TurnSummary{
			N:    i,
			At:   time.Now().Add(-time.Duration(i*4) * 24 * time.Hour), // 4,8,12,16 days ago
			User: "docker 网络问题",
		})
	}

	dp := &dreamProvider{response: "经验"}
	distiller := knowledge.NewDistiller(dp, mgr, expStore)
	dream := knowledge.NewDreamCycle(distiller, mgr, expStore, 3, 3) // lookbackDays=3

	distilled, err := dream.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if len(distilled) != 0 {
		t.Errorf("expected no topics distilled (all turns outside lookback window), got: %v", distilled)
	}
}
