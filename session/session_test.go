package session_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
)

func TestGetCreatesSession(t *testing.T) {
	st := session.NewStore(10, "You are helpful.", "")
	s := st.Get("key1")
	if s == nil {
		t.Fatal("expected non-nil session")
	}
	hist := s.History()
	if len(hist) != 1 || hist[0].Role != "system" {
		t.Errorf("expected system prompt as first message, got %v", hist)
	}
}

func TestGetSameSession(t *testing.T) {
	st := session.NewStore(10, "system", "")
	s1 := st.Get("k")
	s2 := st.Get("k")
	if s1 != s2 {
		t.Error("expected same session pointer for same key")
	}
}

func TestHistoryTrimming(t *testing.T) {
	st := session.NewStore(2, "sys", "") // max 2 turns = 4 non-system messages
	s := st.Get("k")

	for i := 0; i < 6; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		s.Append(provider.Message{Role: role, Content: "msg"}, st.MaxTurns())
	}

	hist := s.History()
	// Should have 1 system + 4 non-system messages.
	nonSys := 0
	for _, m := range hist {
		if m.Role != "system" {
			nonSys++
		}
	}
	if nonSys != 4 {
		t.Errorf("expected 4 non-system messages after trim, got %d (total %d)", nonSys, len(hist))
	}
}

func TestDelete(t *testing.T) {
	st := session.NewStore(10, "sys", "")
	s1 := st.Get("k")
	s1.Append(provider.Message{Role: "user", Content: "hi"}, 10)
	st.Delete("k")

	s2 := st.Get("k")
	// Should be a fresh session with only the system prompt.
	hist := s2.History()
	if len(hist) != 1 {
		t.Errorf("expected fresh session after delete, got %d messages", len(hist))
	}
}

func TestKeys(t *testing.T) {
	st := session.NewStore(10, "sys", "")
	st.Get("a")
	st.Get("b")
	keys := st.Keys()
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestHasAndCreate(t *testing.T) {
	st := session.NewStore(10, "sys", "")

	if st.Has("chat1") {
		t.Error("expected Has to return false before Create")
	}

	sess, err := st.Create("chat1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if sess.Name != "chat1" {
		t.Errorf("expected Name=chat1, got %q", sess.Name)
	}
	if !st.Has("chat1") {
		t.Error("expected Has to return true after Create")
	}

	// Creating the same name again must fail.
	_, err = st.Create("chat1")
	if err == nil {
		t.Error("expected error when creating duplicate session name")
	}
}

func TestTurnCount(t *testing.T) {
	st := session.NewStore(10, "sys", "")
	s, _ := st.Create("c")

	if s.TurnCount() != 0 {
		t.Errorf("expected 0 turns, got %d", s.TurnCount())
	}

	s.Append(provider.Message{Role: "user", Content: "q1"}, 10)
	s.Append(provider.Message{Role: "assistant", Content: "a1"}, 10)
	s.Append(provider.Message{Role: "user", Content: "q2"}, 10)

	if s.TurnCount() != 2 {
		t.Errorf("expected 2 turns (user messages), got %d", s.TurnCount())
	}
}

func TestList(t *testing.T) {
	st := session.NewStore(10, "sys", "")
	st.Create("alpha")
	st.Create("beta")

	sums := st.List()
	if len(sums) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(sums))
	}
	names := map[string]bool{}
	for _, s := range sums {
		names[s.Name] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("expected alpha and beta in list, got %v", names)
	}
}

// ── Persistence tests ─────────────────────────────────────────────────────────

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Write some messages into a session.
	st1 := session.NewStore(20, "sys", dir)
	s, _ := st1.Create("persist-test")
	s.Append(provider.Message{Role: "user", Content: "hello"}, 20)
	s.Append(provider.Message{Role: "assistant", Content: "hi"}, 20)

	// Create a second store backed by the same dir — it should reload the session.
	st2 := session.NewStore(20, "sys", dir)
	if !st2.Has("persist-test") {
		t.Fatal("expected session to be reloaded from disk")
	}
	loaded := st2.Get("persist-test")
	hist := loaded.History()
	// Expect system + user + assistant = 3 messages.
	if len(hist) != 3 {
		t.Errorf("expected 3 messages after reload, got %d", len(hist))
	}
	if loaded.TurnCount() != 1 {
		t.Errorf("expected 1 turn after reload, got %d", loaded.TurnCount())
	}
}

func TestPersistenceDeleteRemovesFile(t *testing.T) {
	dir := t.TempDir()
	st := session.NewStore(10, "", dir)
	st.Create("to-delete")

	// File should exist.
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		t.Fatal("expected session file to be created on disk")
	}

	st.Delete("to-delete")

	// File should be gone.
	entries, _ = os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "to-delete.json" {
			t.Error("expected session file to be deleted from disk")
		}
	}
}

func TestNoPersistenceWhenDirEmpty(t *testing.T) {
	// dir="" means no disk I/O — should still work in-memory.
	st := session.NewStore(10, "sys", "")
	s, _ := st.Create("mem-only")
	s.Append(provider.Message{Role: "user", Content: "test"}, 10)
	if s.TurnCount() != 1 {
		t.Errorf("expected 1 turn, got %d", s.TurnCount())
	}
}

func mkToolCall(id, name string) provider.ToolCallRequest {
	var tc provider.ToolCallRequest
	tc.ID = id
	tc.Type = "function"
	tc.Function.Name = name
	tc.Function.Arguments = `{}`
	return tc
}

func TestTrimPreservesToolTransactionAtomicity(t *testing.T) {
	st := session.NewStore(1, "sys", "")
	s := st.Get("atomic")

	// Turn 1 with tool transaction
	s.Append(provider.Message{Role: "user", Content: "analyze file"}, st.MaxTurns())
	s.Append(provider.Message{Role: "assistant", ToolCalls: []provider.ToolCallRequest{mkToolCall("call-1", "read_file")}}, st.MaxTurns())
	s.Append(provider.Message{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: `{"type":"file_segment"}`}, st.MaxTurns())
	s.Append(provider.Message{Role: "assistant", Content: "done"}, st.MaxTurns())

	// Turn 2 forces turn-1 trimming into summary
	s.Append(provider.Message{Role: "user", Content: "next question"}, st.MaxTurns())
	s.Append(provider.Message{Role: "assistant", Content: "answer"}, st.MaxTurns())

	h := s.History()
	for i, m := range h {
		if m.Role != "tool" {
			continue
		}
		if i == 0 || h[i-1].Role != "assistant" || len(h[i-1].ToolCalls) == 0 {
			t.Fatalf("found orphan tool message at index %d: %+v", i, m)
		}
	}

	foundSummary := false
	for _, m := range h {
		if m.Role == "system" && strings.HasPrefix(m.Content, "[CONTEXT COMPACTION") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Fatal("expected history summary after trimming old turn")
	}
}

func TestTrimByTokenBudgetDropsOldestTurnBlocks(t *testing.T) {
	st := session.NewStore(6, "sys", "")
	st.SetTokenBudget(2200)
	s := st.Get("budget")

	for i := 0; i < 6; i++ {
		long := strings.Repeat("payload ", 90)
		s.Append(provider.Message{Role: "user", Content: fmt.Sprintf("u%d %s", i, long)}, st.MaxTurns())
		s.Append(provider.Message{Role: "assistant", Content: fmt.Sprintf("a%d %s", i, long)}, st.MaxTurns())
	}

	h := s.History()
	if len(h) == 0 {
		t.Fatal("history should not be empty")
	}

	// Oldest user message should have been summarized away under strict token budget.
	for _, m := range h {
		if m.Role == "user" && strings.Contains(m.Content, "u0 ") {
			t.Fatal("expected oldest oversized user messages to be removed by token budget")
		}
	}

	foundSummary := false
	for _, m := range h {
		if m.Role == "system" && strings.HasPrefix(m.Content, "[CONTEXT COMPACTION") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Fatal("expected summary message after token-budget trimming")
	}
}

func TestTrimKeepsConfiguredRecentRawTurns(t *testing.T) {
	st := session.NewStore(6, "sys", "")
	st.SetTokenBudget(2200)
	st.SetRecentRawTurns(2)
	s := st.Get("recent")

	for i := 0; i < 6; i++ {
		long := strings.Repeat("payload ", 90)
		s.Append(provider.Message{Role: "user", Content: fmt.Sprintf("u%d %s", i, long)}, st.MaxTurns())
		s.Append(provider.Message{Role: "assistant", Content: fmt.Sprintf("a%d %s", i, long)}, st.MaxTurns())
	}

	h := s.History()
	userCount := 0
	for _, m := range h {
		if m.Role == "user" {
			userCount++
		}
	}
	if userCount < 2 {
		t.Fatalf("expected at least 2 recent raw turns kept, got %d users", userCount)
	}

	lastUser := ""
	for _, m := range h {
		if m.Role == "user" {
			lastUser = m.Content
		}
	}
	if !strings.Contains(lastUser, "u5") {
		t.Fatalf("expected newest turn to remain in raw history, got last user=%q", lastUser)
	}
}

func TestTrimWithStricterCharsPerTokenEstimator(t *testing.T) {
	stDefault := session.NewStore(6, "sys", "")
	stDefault.SetTokenBudget(2400)
	stDefault.SetRecentRawTurns(1)
	sDefault := stDefault.Get("default-estimator")

	stStrict := session.NewStore(6, "sys", "")
	stStrict.SetTokenBudget(2400)
	stStrict.SetRecentRawTurns(1)
	stStrict.SetCharsPerToken(2)
	sStrict := stStrict.Get("strict-estimator")

	for i := 0; i < 6; i++ {
		long := strings.Repeat("payload ", 100)
		user := fmt.Sprintf("u%d %s", i, long)
		assistant := fmt.Sprintf("a%d %s", i, long)
		sDefault.Append(provider.Message{Role: "user", Content: user}, stDefault.MaxTurns())
		sDefault.Append(provider.Message{Role: "assistant", Content: assistant}, stDefault.MaxTurns())
		sStrict.Append(provider.Message{Role: "user", Content: user}, stStrict.MaxTurns())
		sStrict.Append(provider.Message{Role: "assistant", Content: assistant}, stStrict.MaxTurns())
	}

	countUsers := func(h []provider.Message) int {
		n := 0
		for _, m := range h {
			if m.Role == "user" {
				n++
			}
		}
		return n
	}

	defaultUsers := countUsers(sDefault.History())
	strictUsers := countUsers(sStrict.History())
	if strictUsers > defaultUsers {
		t.Fatalf("expected stricter estimator to keep <= user turns, default=%d strict=%d", defaultUsers, strictUsers)
	}
}

func TestObservePromptUsageAdaptsEstimator(t *testing.T) {
	control := session.NewStore(6, "sys", "")
	control.SetTokenBudget(2400)
	control.SetRecentRawTurns(1)
	controlSession := control.Get("control-adaptive")

	adaptive := session.NewStore(6, "sys", "")
	adaptive.SetTokenBudget(2400)
	adaptive.SetRecentRawTurns(1)
	adaptiveSession := adaptive.Get("adaptive")

	probe := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("dense", 120)},
		{Role: "assistant", Content: strings.Repeat("dense", 120)},
	}
	adaptive.ObservePromptUsage(probe, 1200)

	for i := 0; i < 6; i++ {
		long := strings.Repeat("payload ", 100)
		user := fmt.Sprintf("u%d %s", i, long)
		assistant := fmt.Sprintf("a%d %s", i, long)
		controlSession.Append(provider.Message{Role: "user", Content: user}, control.MaxTurns())
		controlSession.Append(provider.Message{Role: "assistant", Content: assistant}, control.MaxTurns())
		adaptiveSession.Append(provider.Message{Role: "user", Content: user}, adaptive.MaxTurns())
		adaptiveSession.Append(provider.Message{Role: "assistant", Content: assistant}, adaptive.MaxTurns())
	}

	countUsers := func(h []provider.Message) int {
		n := 0
		for _, m := range h {
			if m.Role == "user" {
				n++
			}
		}
		return n
	}

	controlUsers := countUsers(controlSession.History())
	adaptiveUsers := countUsers(adaptiveSession.History())
	if adaptiveUsers > controlUsers {
		t.Fatalf("expected adaptive estimator to keep <= user turns after dense prompt observation, control=%d adaptive=%d", controlUsers, adaptiveUsers)
	}
}

func TestObservePromptUsageDoesNotOverrideFixedEstimator(t *testing.T) {
	control := session.NewStore(6, "sys", "")
	control.SetTokenBudget(2400)
	control.SetRecentRawTurns(1)
	control.SetCharsPerToken(6)
	controlSession := control.Get("fixed-control")

	target := session.NewStore(6, "sys", "")
	target.SetTokenBudget(2400)
	target.SetRecentRawTurns(1)
	target.SetCharsPerToken(6)
	target.ObservePromptUsage([]provider.Message{{Role: "user", Content: strings.Repeat("abcd", 200)}}, 1200)
	targetSession := target.Get("fixed-target")

	for i := 0; i < 6; i++ {
		long := strings.Repeat("payload ", 100)
		user := fmt.Sprintf("u%d %s", i, long)
		assistant := fmt.Sprintf("a%d %s", i, long)
		controlSession.Append(provider.Message{Role: "user", Content: user}, control.MaxTurns())
		controlSession.Append(provider.Message{Role: "assistant", Content: assistant}, control.MaxTurns())
		targetSession.Append(provider.Message{Role: "user", Content: user}, target.MaxTurns())
		targetSession.Append(provider.Message{Role: "assistant", Content: assistant}, target.MaxTurns())
	}

	countUsers := func(h []provider.Message) int {
		n := 0
		for _, m := range h {
			if m.Role == "user" {
				n++
			}
		}
		return n
	}

	if countUsers(controlSession.History()) != countUsers(targetSession.History()) {
		t.Fatal("expected fixed estimator behavior to remain unchanged after ObservePromptUsage")
	}
}

func TestHistoryForLLMUsesHintBudgetScale(t *testing.T) {
	st := session.NewStore(8, "sys", "")
	st.SetTokenBudget(2600)
	st.SetRecentRawTurns(1)
	st.SetHintBudgetScale(provider.ModelHintRouter, 0.35)
	st.SetHintBudgetScale(provider.ModelHintThinking, 1.6)
	s := st.Get("hint-aware")

	for i := 0; i < 8; i++ {
		long := strings.Repeat("payload ", 100)
		s.Append(provider.Message{Role: "user", Content: fmt.Sprintf("u%d %s", i, long)}, st.MaxTurns())
		s.Append(provider.Message{Role: "assistant", Content: fmt.Sprintf("a%d %s", i, long)}, st.MaxTurns())
	}

	countUsers := func(h []provider.Message) int {
		n := 0
		for _, m := range h {
			if m.Role == "user" {
				n++
			}
		}
		return n
	}

	routerUsers := countUsers(s.HistoryForLLM(provider.ModelHintRouter))
	thinkingUsers := countUsers(s.HistoryForLLM(provider.ModelHintThinking))
	if thinkingUsers < routerUsers {
		t.Fatalf("expected thinking history to keep >= router history, router=%d thinking=%d", routerUsers, thinkingUsers)
	}
	if routerUsers == thinkingUsers {
		t.Fatalf("expected hint budget scale to produce different retained history, router=%d thinking=%d", routerUsers, thinkingUsers)
	}
}

func TestHistoryForLLMExpandsDateInSystemPrompt(t *testing.T) {
	st := session.NewStore(10, "Today is {date}. Hello.", "")
	s := st.Get("expand-test")

	llmHist := s.HistoryForLLM(provider.ModelHintTask)
	if len(llmHist) == 0 || llmHist[0].Role != "system" {
		t.Fatal("expected system message in HistoryForLLM result")
	}
	if strings.Contains(llmHist[0].Content, "{date}") {
		t.Errorf("HistoryForLLM should expand {date}, but got: %q", llmHist[0].Content)
	}

	// History() must still return the raw template (not expanded)
	rawHist := s.History()
	if len(rawHist) == 0 || rawHist[0].Role != "system" {
		t.Fatal("expected system message in History result")
	}
	if !strings.Contains(rawHist[0].Content, "{date}") {
		t.Errorf("History() should keep raw template with {date}, but got: %q", rawHist[0].Content)
	}
}

func TestHistoryForLLMExpandsDatetimeInSystemPrompt(t *testing.T) {
	st := session.NewStore(10, "now={datetime}", "")
	s := st.Get("expand-datetime")

	llmHist := s.HistoryForLLM(provider.ModelHintTask)
	if len(llmHist) == 0 || llmHist[0].Role != "system" {
		t.Fatal("expected system message in HistoryForLLM result")
	}
	if strings.Contains(llmHist[0].Content, "{datetime}") {
		t.Errorf("HistoryForLLM should expand {datetime}, but got: %q", llmHist[0].Content)
	}
}

func TestHistoryForLLMDoesNotExpandNonTimeVars(t *testing.T) {
	st := session.NewStore(10, "cwd={cwd} os={os} user={user}", "")
	s := st.Get("no-expand-non-time")

	llmHist := s.HistoryForLLM(provider.ModelHintTask)
	if len(llmHist) == 0 || llmHist[0].Role != "system" {
		t.Fatal("expected system message in HistoryForLLM result")
	}
	content := llmHist[0].Content
	if !strings.Contains(content, "{cwd}") {
		t.Errorf("HistoryForLLM should NOT expand {cwd}, but got: %q", content)
	}
	if !strings.Contains(content, "{os}") {
		t.Errorf("HistoryForLLM should NOT expand {os}, but got: %q", content)
	}
	if !strings.Contains(content, "{user}") {
		t.Errorf("HistoryForLLM should NOT expand {user}, but got: %q", content)
	}
}

func TestTrimPrefersKeepingToolHeavyTurns(t *testing.T) {
	st := session.NewStore(4, "sys", "")
	st.SetTokenBudget(2000)
	st.SetRecentRawTurns(1)
	st.SetHintBudgetScale(provider.ModelHintTask, 0.5)
	s := st.Get("tool-priority")

	plainLong := strings.Repeat("plain ", 180)
	toolLong := strings.Repeat("tool ", 180)
	// newestLong is shorter so that after the plain turn is trimmed the remaining
	// turn2 (tool-heavy) + turn3 (newest) fit within the scaled budget (1000 tokens),
	// confirming that tool-heavy turns are spared once plain turns are gone first.
	newestLong := strings.Repeat("newest ", 80)

	// Oldest plain turn should be trimmed before the later tool-heavy turn.
	s.Append(provider.Message{Role: "user", Content: "plain-user " + plainLong}, st.MaxTurns())
	s.Append(provider.Message{Role: "assistant", Content: "plain-assistant " + plainLong}, st.MaxTurns())

	s.Append(provider.Message{Role: "user", Content: "tool-user " + toolLong}, st.MaxTurns())
	s.Append(provider.Message{Role: "assistant", ToolCalls: []provider.ToolCallRequest{mkToolCall("call-keep", "read_file")}}, st.MaxTurns())
	s.Append(provider.Message{Role: "tool", ToolCallID: "call-keep", Name: "read_file", Content: `{"type":"tool_result"}`}, st.MaxTurns())
	s.Append(provider.Message{Role: "assistant", Content: "tool-assistant " + toolLong}, st.MaxTurns())

	s.Append(provider.Message{Role: "user", Content: "newest-user " + newestLong}, st.MaxTurns())
	s.Append(provider.Message{Role: "assistant", Content: "newest-assistant " + newestLong}, st.MaxTurns())

	h := s.HistoryForLLM(provider.ModelHintTask)
	joined := ""
	for _, m := range h {
		joined += m.Content + "\n"
	}
	if strings.Contains(joined, "plain-user "+plainLong) {
		t.Fatal("expected oldest plain turn to be trimmed before tool-heavy turn")
	}
	if !strings.Contains(joined, "tool-user "+toolLong) {
		t.Fatal("expected tool-heavy turn to be retained under the same budget")
	}
	foundToolMessage := false
	for _, m := range h {
		if m.Role == "tool" && m.Name == "read_file" {
			foundToolMessage = true
			break
		}
	}
	if !foundToolMessage {
		t.Fatal("expected tool result to remain when tool-heavy turn is prioritized")
	}
}

func TestBuildSummaryMessageNewPrefix(t *testing.T) {
	st := session.NewStore(10, "sys", "")
	s := st.Get("k")
	for i := 0; i < 15; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		s.Append(provider.Message{Role: role, Content: fmt.Sprintf("msg%d", i)}, st.MaxTurns())
	}
	hist := s.HistoryForLLM(provider.ModelHintTask)
	for _, m := range hist {
		if m.Role == "system" && strings.Contains(m.Content, "[History summary]") {
			t.Errorf("old prefix found; expected CONTEXT COMPACTION prefix")
		}
	}
}

func TestRefreshSummaryCacheAffectsTrimOutput(t *testing.T) {
	// maxTurns=2: HistoryForLLM will compress turns beyond the last 2.
	// We add 4 turns so turns 1 and 2 get compressed into the summary block.
	// RefreshSummaryCache is called before HistoryForLLM so LLM summaries are
	// available when trimMessages runs.
	st := session.NewStore(2, "sys", "")
	s := st.Get("sess1")

	for i := 1; i <= 4; i++ {
		// Use Append with maxTurns=100 so Append itself does NOT trim.
		s.Append(provider.Message{Role: "user", Content: fmt.Sprintf("question %d", i)}, 100)
		s.Append(provider.Message{Role: "assistant", Content: fmt.Sprintf("answer %d", i)}, 100)
	}

	// Inject LLM summaries before HistoryForLLM compresses turns 1 and 2.
	turns := []memory.TurnSummary{
		{N: 1, LLMSummary: "LLM summary for turn one"},
		{N: 2, LLMSummary: "LLM summary for turn two"},
	}
	st.RefreshSummaryCache("sess1", turns)

	// HistoryForLLM uses maxTurns=2, so it compresses turns 1 and 2 into summary.
	hist := s.HistoryForLLM(provider.ModelHintTask)

	found := false
	for _, m := range hist {
		if m.Role == "system" && strings.Contains(m.Content, "CONTEXT COMPACTION") {
			found = true
			if !strings.Contains(m.Content, "LLM summary for turn") {
				t.Errorf("expected LLM summary text in compaction block, got:\n%s", m.Content)
			}
			if strings.Contains(m.Content, "U:question 1") {
				t.Errorf("found rule-based fallback for turn 1; LLM summary should take precedence:\n%s", m.Content)
			}
		}
	}
	if !found {
		t.Error("expected CONTEXT COMPACTION block in HistoryForLLM output")
	}
}
