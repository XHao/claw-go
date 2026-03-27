package agent

import (
	"testing"

	"github.com/XHao/claw-go/provider"
)

func mkToolCall(id, name string) provider.ToolCallRequest {
	var tc provider.ToolCallRequest
	tc.ID = id
	tc.Type = "function"
	tc.Function.Name = name
	tc.Function.Arguments = `{}`
	return tc
}

func TestPrepareMessagesForLLMInjectsFileGuidanceAfterSystem(t *testing.T) {
	history := []provider.Message{
		{Role: "system", Content: "base system"},
		{Role: "user", Content: "analyze this flamegraph"},
	}
	tools := []provider.ToolDef{
		{Name: "inspect_file"},
		{Name: "read_file"},
		{Name: "search_file"},
	}

	prepared := prepareMessagesForLLM(history, tools)
	if len(prepared) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(prepared))
	}
	if prepared[1].Role != "system" {
		t.Fatalf("expected injected guidance as system message, got role=%s", prepared[1].Role)
	}
	if prepared[1].Content != fileToolSelectionPrompt {
		t.Fatalf("expected injected file tool guidance, got: %s", prepared[1].Content)
	}
	if prepared[2].Content != "analyze this flamegraph" {
		t.Fatalf("expected user message preserved, got: %s", prepared[2].Content)
	}
}

func TestPrepareMessagesForLLMNoInjectionWithoutWorkflowTools(t *testing.T) {
	history := []provider.Message{{Role: "user", Content: "hello"}}
	tools := []provider.ToolDef{{Name: "read_file"}}

	prepared := prepareMessagesForLLM(history, tools)
	if len(prepared) != len(history) {
		t.Fatalf("expected unchanged message count, got %d", len(prepared))
	}
	if prepared[0].Content != history[0].Content {
		t.Fatalf("expected original message preserved, got: %s", prepared[0].Content)
	}
}

func TestHasFileToolWorkflow(t *testing.T) {
	if !hasFileToolWorkflow([]provider.ToolDef{{Name: "inspect_file"}, {Name: "read_file"}, {Name: "search_file"}}) {
		t.Fatal("expected workflow tools to be detected")
	}
	if hasFileToolWorkflow([]provider.ToolDef{{Name: "inspect_file"}, {Name: "read_file"}}) {
		t.Fatal("expected incomplete workflow to be rejected")
	}
}

func TestSanitizeHistoryForToolProtocolDropsOrphanToolMessage(t *testing.T) {
	history := []provider.Message{
		{Role: "tool", ToolCallID: "function-call-1", Name: "read_file", Content: `{"type":"tool_result"}`},
		{Role: "user", Content: "hello"},
	}

	got := sanitizeHistoryForToolProtocol(history)
	if len(got) != 1 {
		t.Fatalf("expected orphan tool message to be dropped, got len=%d", len(got))
	}
	if got[0].Role != "user" {
		t.Fatalf("expected remaining message to be user, got role=%s", got[0].Role)
	}
}

func TestSanitizeHistoryForToolProtocolKeepsValidToolSequence(t *testing.T) {
	toolCall := mkToolCall("function-call-1", "read_file")
	history := []provider.Message{
		{Role: "assistant", ToolCalls: []provider.ToolCallRequest{toolCall}},
		{Role: "tool", ToolCallID: "function-call-1", Name: "read_file", Content: `{"type":"tool_result"}`},
		{Role: "assistant", Content: "done"},
	}

	got := sanitizeHistoryForToolProtocol(history)
	if len(got) != 3 {
		t.Fatalf("expected valid sequence preserved, got len=%d", len(got))
	}
	if got[0].Role != "assistant" || len(got[0].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call message preserved, got %+v", got[0])
	}
	if got[1].Role != "tool" || got[1].ToolCallID != "function-call-1" {
		t.Fatalf("expected matching tool result preserved, got %+v", got[1])
	}
}

func TestSanitizeHistoryOrphanAssistantToolCallsAtEndDropped(t *testing.T) {
	// Daemon crashed after appending assistant(tool_calls) but before tool results.
	tc := mkToolCall("tc-1", "read_file")
	history := []provider.Message{
		{Role: "user", Content: "check file"},
		{Role: "assistant", ToolCalls: []provider.ToolCallRequest{tc}},
		// tool result never appended
	}
	got := sanitizeHistoryForToolProtocol(history)
	if len(got) != 1 || got[0].Role != "user" {
		t.Fatalf("expected only user message after dropping orphaned assistant(tool_calls), got len=%d %+v", len(got), got)
	}
}

func TestSanitizeHistoryOrphanAssistantToolCallsFollowedByUser(t *testing.T) {
	// Incomplete turn in middle: new user message arrives before tool results were stored.
	tc := mkToolCall("tc-1", "read_file")
	history := []provider.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", ToolCalls: []provider.ToolCallRequest{tc}},
		// tool result missing
		{Role: "user", Content: "second"},
	}
	got := sanitizeHistoryForToolProtocol(history)
	// The orphaned assistant block (and its missing tool result) must be stripped;
	// both user messages should remain.
	if len(got) != 2 {
		t.Fatalf("expected 2 user messages, got len=%d %+v", len(got), got)
	}
	if got[0].Content != "first" || got[1].Content != "second" {
		t.Fatalf("unexpected message contents: %+v", got)
	}
}

func TestSanitizeHistoryCompleteToolSequenceThenIncompleteDropsLast(t *testing.T) {
	// First turn is complete; second turn's tool results are missing.
	tc1 := mkToolCall("tc-1", "read_file")
	tc2 := mkToolCall("tc-2", "grep_file")
	history := []provider.Message{
		{Role: "user", Content: "turn1"},
		{Role: "assistant", ToolCalls: []provider.ToolCallRequest{tc1}},
		{Role: "tool", ToolCallID: "tc-1", Name: "read_file", Content: "ok"},
		{Role: "assistant", Content: "done with turn1"},
		{Role: "user", Content: "turn2"},
		{Role: "assistant", ToolCalls: []provider.ToolCallRequest{tc2}},
		// tool result for tc-2 is missing
	}
	got := sanitizeHistoryForToolProtocol(history)
	// The incomplete second assistant block should be dropped; everything before it kept.
	if len(got) != 5 {
		t.Fatalf("expected 5 messages (first full turn + user2), got len=%d", len(got))
	}
	if got[4].Content != "turn2" {
		t.Fatalf("expected last message to be user turn2, got %+v", got[4])
	}
}
