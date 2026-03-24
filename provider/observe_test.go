package provider

import (
	"context"
	"testing"
)

type stubObserveProvider struct {
	result CompleteResult
	err    error
}

func (s *stubObserveProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	return s.result.Content, s.err
}

func (s *stubObserveProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	return s.result, s.err
}

func TestWrapObserveEmitsUsageEvent(t *testing.T) {
	inner := &stubObserveProvider{result: CompleteResult{
		Usage:      Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30},
		StopReason: "stop",
		Model:      ModelMeta{ModelKey: "task_model", Model: "gpt-4o"},
	}}
	obs := WrapObserve(inner)

	var got UsageEvent
	ctx := WithUsageObserver(WithHintSource(WithModelHint(context.Background(), ModelHintTask), HintSourceAgentLoop(0)), func(ev UsageEvent) {
		got = ev
	})

	msgs := []Message{
		{Role: "system", Content: "You are a helpful coding assistant."},
		{Role: "user", Content: "请帮我设计一个 API"},
	}
	_, err := obs.CompleteWithTools(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Hint != string(ModelHintTask) {
		t.Fatalf("want hint task, got %q", got.Hint)
	}
	if got.Source != "agent/loop[i=0]" {
		t.Fatalf("want source agent/loop[i=0], got %q", got.Source)
	}
	if got.ModelKey != "task_model" || got.Model != "gpt-4o" {
		t.Fatalf("unexpected model meta: %+v", got)
	}
	if got.TotalTokens != 30 {
		t.Fatalf("want total tokens 30, got %d", got.TotalTokens)
	}
	if got.InputTokensEst+got.ContextTokensEst != got.PromptTokens {
		t.Fatalf("prompt split mismatch: input=%d context=%d prompt=%d", got.InputTokensEst, got.ContextTokensEst, got.PromptTokens)
	}
}
