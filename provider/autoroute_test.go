package provider

import (
	"context"
	"errors"
	"testing"
	"time"
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

// slowProvider blocks until the context is cancelled—used to test timeout behaviour.
type slowProvider struct{}

func (s slowProvider) Complete(ctx context.Context, _ []Message) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (s slowProvider) CompleteWithTools(ctx context.Context, _ []Message, _ []ToolDef) (CompleteResult, error) {
	<-ctx.Done()
	return CompleteResult{}, ctx.Err()
}

// TestAutoRouterPhase2TimeoutDegradesToTask: when the routing model does not
// respond within classifyTimeout the result must be a conservative task decision,
// not a hang.
func TestAutoRouterPhase2TimeoutDegradesToTask(t *testing.T) {
	a := NewAutoRouter(slowProvider{}, nil)
	a.classifyTimeout = 40 * time.Millisecond

	// Long, ambiguous text with no thinking keywords — bypasses Phase-1 and forces Phase 2.
	text := "can you explain in detail how the payment processing flow works across the checkout module and what edge cases exist"
	d := a.Classify(context.Background(), text, []Message{{Role: "user", Content: text}}, nil)

	if d.Hint != ModelHintTask {
		t.Fatalf("want task (timeout fallback), got %q (%s)", d.Hint, d.Reason)
	}
	if d.ReasonCode != "classifier_error" {
		t.Fatalf("want classifier_error on timeout, got %q", d.ReasonCode)
	}
}

// errorProvider simulates a 429-style failure that would normally trigger FallbackProvider.
type errorProvider struct {
	err      error
	callSeen *bool
}

func (e errorProvider) Complete(_ context.Context, _ []Message) (string, error) {
	if e.callSeen != nil {
		*e.callSeen = true
	}
	return "", e.err
}

func (e errorProvider) CompleteWithTools(_ context.Context, _ []Message, _ []ToolDef) (CompleteResult, error) {
	if e.callSeen != nil {
		*e.callSeen = true
	}
	return CompleteResult{}, e.err
}

// TestFallbackProviderSkippedWhenNoFallbackSet: a 429 on the routing model must
// NOT reach the fallback (task) provider; AutoRouter must degrade to task tier
// directly via "classifier_error" without using the expensive fallback model.
func TestFallbackProviderSkippedWhenNoFallbackSet(t *testing.T) {
	fallbackCalled := false
	primary := errorProvider{err: errors.New("status 429 too many requests")}
	// fallback provider has no error — it would succeed if called.
	fallback := errorProvider{callSeen: &fallbackCalled}

	wrapped := WrapFallback(primary, fallback)

	// Without noFallback flag the fallback IS reached on 429.
	_, _ = wrapped.Complete(context.Background(), nil)
	if !fallbackCalled {
		t.Fatal("expected fallback to be called without noFallback flag")
	}

	// With noFallback the fallback must be skipped and the 429 error must propagate.
	fallbackCalled = false
	noFBCtx := withNoFallback(context.Background())
	_, err := wrapped.Complete(noFBCtx, nil)
	if err == nil {
		t.Fatal("expected 429 error to propagate when noFallback is set")
	}
	if fallbackCalled {
		t.Fatal("fallback must not be called when noFallback flag is set")
	}
}

// TestAutoRouterPhase2NoFallbackPropagated: when Phase 2 triggers, the classify
// context carries the noFallback flag so FallbackProvider is bypassed.
func TestAutoRouterPhase2NoFallbackPropagated(t *testing.T) {
	fallbackCalled := false
	primary := errorProvider{err: errors.New("status 429 too many requests")}
	fallbackSpy := errorProvider{callSeen: &fallbackCalled}

	wrapped := WrapFallback(primary, fallbackSpy)
	a := NewAutoRouter(wrapped, nil)

	// Long, ambiguous text with no thinking keywords — forces Phase 2.
	text := "can you explain in detail how the payment processing flow works across the checkout module and what edge cases exist"
	d := a.Classify(context.Background(), text, []Message{{Role: "user", Content: text}}, nil)

	if d.Hint != ModelHintTask || d.ReasonCode != "classifier_error" {
		t.Fatalf("want task/classifier_error, got hint=%q reason=%q", d.Hint, d.ReasonCode)
	}
	if fallbackCalled {
		t.Fatal("FallbackProvider must not be called during Phase 2 classification")
	}
}
