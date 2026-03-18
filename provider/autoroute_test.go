package provider

import (
	"context"
	"testing"
)

type fakeProvider struct{}

func (f fakeProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	return `{"tier":"task","reason_code":"simple_request","confidence":0.8}`, nil
}

func (f fakeProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	return CompleteResult{Content: `{"tier":"task","reason_code":"simple_request","confidence":0.8}`}, nil
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

func TestAutoRouterLLMDecisionCarriesStructuredFields(t *testing.T) {
	a := NewAutoRouter(fakeProvider{}, nil)
	text := "Please summarize the current request handling behavior across multiple modules and explain it in plain detail for a new teammate"
	d := a.Classify(context.Background(), text, []Message{{Role: "user", Content: text}}, nil)
	if d.Hint != ModelHintTask {
		t.Fatalf("want task, got %q (%s)", d.Hint, d.Reason)
	}
	if d.ReasonCode != "simple_request" {
		t.Fatalf("want reason_code simple_request, got %q", d.ReasonCode)
	}
	if d.Confidence != 0.8 {
		t.Fatalf("want confidence 0.8, got %v", d.Confidence)
	}
}
