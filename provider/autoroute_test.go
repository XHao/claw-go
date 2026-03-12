package provider

import (
	"context"
	"testing"
)

type fakeProvider struct{}

func (f fakeProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	return `{"tier":"task"}`, nil
}

func (f fakeProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	return CompleteResult{Content: `{"tier":"task"}`}, nil
}

func TestAutoRouterBuiltinKeywordTriggersThinking(t *testing.T) {
	a := NewAutoRouter(fakeProvider{}, nil)
	d := a.Classify(context.Background(), "请帮我做系统架构重构", nil, nil)
	if d.Hint != ModelHintThinking {
		t.Fatalf("want thinking, got %q (%s)", d.Hint, d.Reason)
	}
}

func TestAutoRouterCustomKeywordTriggersThinking(t *testing.T) {
	a := NewAutoRouter(fakeProvider{}, []string{"alpha-beta"})
	d := a.Classify(context.Background(), "Need alpha-beta plan", nil, nil)
	if d.Hint != ModelHintThinking {
		t.Fatalf("want thinking, got %q (%s)", d.Hint, d.Reason)
	}
}
