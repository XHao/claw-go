package agent

import (
	"testing"

	"github.com/XHao/claw-go/provider"
)

func TestPrepareMessagesForLLMInjectsFileGuidanceAfterSystem(t *testing.T) {
	history := []provider.Message{
		{Role: "system", Content: "base system"},
		{Role: "user", Content: "analyze this flamegraph"},
	}
	tools := []provider.ToolDef{
		{Name: "inspect_file"},
		{Name: "read_file"},
		{Name: "grep_file"},
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
