package knowledge_test

import (
	"strings"
	"testing"

	"github.com/XHao/claw-go/knowledge"
)

func TestReduceSystemPrompt_ContainsConflictInstructions(t *testing.T) {
	prompt := knowledge.ReduceSystemPromptForTest()
	if !strings.Contains(prompt, "[已更新]") {
		t.Error("reduceSystemPrompt should instruct LLM to annotate updated items with [已更新]")
	}
	if !strings.Contains(prompt, "[已废弃]") {
		t.Error("reduceSystemPrompt should instruct LLM to annotate deprecated items with [已废弃]")
	}
}

func TestEvalSystemPrompt_ContainsConflictInstructions(t *testing.T) {
	prompt := knowledge.EvalSystemPromptForTest()
	if !strings.Contains(prompt, "更新：") {
		t.Error("evalSystemPrompt should instruct LLM to prefix conflict summaries with '更新：'")
	}
}
