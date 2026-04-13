package memory_test

import (
	"testing"
	"time"

	. "github.com/XHao/claw-go/memory"
)

func TestManager_SearchTurns_ReturnsRelevantTurns(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	sess := mgr.ForSession("s1")
	_ = sess.SaveTurn(TurnSummary{N: 1, At: time.Now(), User: "如何配置 docker compose 网络", Reply: "使用 bridge 模式"})
	_ = sess.SaveTurn(TurnSummary{N: 2, At: time.Now(), User: "golang goroutine 泄漏怎么排查", Reply: "用 pprof"})

	results, err := mgr.SearchTurns([]string{"docker"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if results[0].N != 1 {
		t.Errorf("want turn N=1, got N=%d", results[0].N)
	}
}

func TestManager_SearchTurns_RespectsMaxResults(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	sess := mgr.ForSession("s1")
	for i := 1; i <= 5; i++ {
		_ = sess.SaveTurn(TurnSummary{N: i, At: time.Now(), User: "docker 问题"})
	}

	results, err := mgr.SearchTurns([]string{"docker"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("want exactly 3 results, got %d", len(results))
	}
}

func TestManager_SearchTurns_CrossSession(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)

	_ = mgr.ForSession("s1").SaveTurn(TurnSummary{N: 1, At: time.Now(), User: "docker 网络配置"})
	_ = mgr.ForSession("s2").SaveTurn(TurnSummary{N: 1, At: time.Now(), User: "docker compose 部署"})
	_ = mgr.ForSession("s3").SaveTurn(TurnSummary{N: 1, At: time.Now(), User: "golang 并发模型"})

	results, err := mgr.SearchTurns([]string{"docker"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Errorf("want 2 results from 2 sessions, got %d", len(results))
	}
}

func TestManager_SearchTurns_RecentBeatsOlder(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	sess := mgr.ForSession("s1")

	// 旧记录：90 天前，命中 3 次
	_ = sess.SaveTurn(TurnSummary{
		N:    1,
		At:   time.Now().Add(-90 * 24 * time.Hour),
		User: "docker docker docker 网络配置问题",
	})
	// 新记录：1 天前，命中 1 次
	_ = sess.SaveTurn(TurnSummary{
		N:    2,
		At:   time.Now().Add(-24 * time.Hour),
		User: "docker 容器启动失败",
	})

	results, err := mgr.SearchTurns([]string{"docker"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	// 旧记录命中 3 次但 90 天前，复合分 = 3 × 0.25 = 0.75
	// 新记录命中 1 次但 1 天前，复合分 = 1 × 0.97 ≈ 0.97
	if results[0].N != 2 {
		t.Errorf("want recent turn (N=2) first, got N=%d first", results[0].N)
	}
}
