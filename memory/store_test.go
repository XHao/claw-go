package memory_test

import (
	"encoding/json"
	"os"
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

func TestTurnSummaryLLMSummaryOmitempty(t *testing.T) {
	// 旧格式 JSON 不含 llm_summary 字段
	raw := `{"n":1,"at":"2024-01-01T00:00:00Z","user":"hello","reply":"world"}`
	var ts TurnSummary
	if err := json.Unmarshal([]byte(raw), &ts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ts.LLMSummary != "" {
		t.Errorf("expected empty LLMSummary, got %q", ts.LLMSummary)
	}

	// 新格式含 llm_summary
	raw2 := `{"n":2,"at":"2024-01-01T00:00:00Z","user":"hi","reply":"ok","llm_summary":"- did X"}`
	var ts2 TurnSummary
	if err := json.Unmarshal([]byte(raw2), &ts2); err != nil {
		t.Fatalf("unmarshal2: %v", err)
	}
	if ts2.LLMSummary != "- did X" {
		t.Errorf("expected '- did X', got %q", ts2.LLMSummary)
	}
}

func TestUpdateSummary(t *testing.T) {
	dir := t.TempDir()
	s := NewManager(dir).ForSession("sess")

	orig := TurnSummary{N: 1, At: time.Now().UTC(), User: "hello", Reply: "world"}
	if err := s.SaveTurn(orig); err != nil {
		t.Fatalf("SaveTurn: %v", err)
	}

	patch := orig
	patch.LLMSummary = "- user asked hello\n- replied world"
	if err := s.UpdateSummary(patch); err != nil {
		t.Fatalf("UpdateSummary: %v", err)
	}

	turns, err := s.LoadRecent(0)
	if err != nil {
		t.Fatalf("LoadRecent: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].LLMSummary != "- user asked hello\n- replied world" {
		t.Errorf("expected merged LLMSummary, got %q", turns[0].LLMSummary)
	}
}

func TestCompactDay(t *testing.T) {
	dir := t.TempDir()
	s := NewManager(dir).ForSession("sess")

	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	// 写两条原始记录 + 一条补丁
	_ = s.SaveTurn(TurnSummary{N: 1, At: day, User: "hello", Reply: "world"})
	_ = s.SaveTurn(TurnSummary{N: 2, At: day.Add(time.Hour), User: "foo", Reply: "bar"})
	patch := TurnSummary{N: 1, At: day, LLMSummary: "- patched"}
	_ = s.UpdateSummary(patch)

	// 压实
	if err := s.CompactDay("2024-01-01"); err != nil {
		t.Fatalf("CompactDay: %v", err)
	}

	// 压实后 LoadRecent 仍能正确读取，且 N=1 含 llm_summary
	turns, err := s.LoadRecent(0)
	if err != nil {
		t.Fatalf("LoadRecent after compact: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns after compact, got %d", len(turns))
	}
	if turns[0].LLMSummary != "- patched" {
		t.Errorf("expected patched llm_summary, got %q", turns[0].LLMSummary)
	}
}

func TestPruneOldFilesViaRetainDays(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(dir)
	mgr.SetRetainDays(90)
	s := mgr.ForSession("sess")

	now := time.Date(2024, 6, 10, 0, 0, 0, 0, time.UTC)

	// Pre-create JSONL files at various ages directly in the session dir.
	sessDir := dir + "/sess"
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, daysAgo := range []int{100, 91, 90, 89, 1} {
		day := now.AddDate(0, 0, -daysAgo)
		path := sessDir + "/" + day.Format("2006-01-02") + ".jsonl"
		if err := os.WriteFile(path, []byte(`{"n":1,"at":"`+day.Format(time.RFC3339)+`","user":"x","reply":"y"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// SaveTurn with a "tomorrow" date triggers the prune goroutine.
	// Use a date far enough in the future that the goroutine fires quickly.
	tomorrow := now.AddDate(0, 0, 1)
	_ = s.SaveTurn(TurnSummary{N: 99, At: tomorrow, User: "trigger", Reply: "prune"})

	// Give the goroutine time to run.
	time.Sleep(100 * time.Millisecond)

	days, err := s.ListDays()
	if err != nil {
		t.Fatal(err)
	}
	// Should keep: 90, 89, 1 days ago + the new "tomorrow" file = 4 files.
	// Should delete: 100 and 91 days ago (both > 90 days before now).
	for _, d := range days {
		if d == now.AddDate(0, 0, -100).Format("2006-01-02") {
			t.Errorf("expected file for 100 days ago to be pruned, but it still exists")
		}
		if d == now.AddDate(0, 0, -91).Format("2006-01-02") {
			t.Errorf("expected file for 91 days ago to be pruned, but it still exists")
		}
	}
}

func TestSaveTurnWithAgentID(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(tmp)
	store := mgr.ForSession("main")

	turn := TurnSummary{
		N:       1,
		At:      time.Now(),
		User:    "hello",
		Reply:   "world",
		AgentID: "lawyer",
	}
	if err := store.SaveTurn(turn); err != nil {
		t.Fatalf("SaveTurn error: %v", err)
	}

	turns, err := store.LoadRecent(0)
	if err != nil {
		t.Fatalf("LoadRecent error: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0].AgentID != "lawyer" {
		t.Errorf("AgentID = %q, want 'lawyer'", turns[0].AgentID)
	}
}

func TestLoadRecentFilterByAgent(t *testing.T) {
	tmp := t.TempDir()
	mgr := NewManager(tmp)
	store := mgr.ForSession("main")

	for i, agentID := range []string{"lawyer", "coder", "lawyer"} {
		store.SaveTurn(TurnSummary{
			N: i + 1, At: time.Now(), User: "q", AgentID: agentID,
		})
	}

	turns, _ := store.LoadRecentForAgent(0, "lawyer")
	if len(turns) != 2 {
		t.Errorf("expected 2 lawyer turns, got %d", len(turns))
	}
	for _, t2 := range turns {
		if t2.AgentID != "lawyer" {
			t.Errorf("got AgentID %q, want 'lawyer'", t2.AgentID)
		}
	}
}
