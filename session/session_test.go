package session_test

import (
	"os"
	"testing"

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
