package provider

import (
	"context"
	"fmt"
	"log/slog"
)

// RouterProviderConfig describes a single tier within a RouterProvider.
// It mirrors ProviderConfig but is defined here to avoid an import cycle.
// main.go constructs RouterProvider by passing already-built Provider instances.
type RouterProviderConfig struct {
	BaseURL        string
	APIKey         string
	Model          string
	MaxTokens      int
	TimeoutSeconds int
}

// RouterProvider implements Provider by dispatching each call to one of four
// model tiers based on the ModelHint stored in the request context.
//
//   - routing  (Tier 1): ultra-cheap intent classifier — skill dispatch
//   - task     (Tier 2): general-purpose model — normal conversation (default)
//   - summary  (Tier 2): long-context summariser — distillation, log triage
//   - thinking (Tier 3): deep-reasoning model — complex coding, architecture
//
// Optional tiers (routing, summary, thinking) may be nil; when a call arrives
// with a hint for an unconfigured tier, RouterProvider falls back to task and
// emits a WARN log so the degradation is visible in the daemon output.
type RouterProvider struct {
	routing  Provider // Tier 1 — nil → task (with warn)
	task     Provider // Tier 2 (required)
	summary  Provider // Tier 2b — nil → task (with warn)
	thinking Provider // Tier 3 — nil → task (with warn)
}

// NewRouterProvider builds a RouterProvider from pre-constructed Provider
// instances.  Only task is required; pass nil for optional tiers to use task
// as a fallback (a WARN is logged at call time when fallback is used).
func NewRouterProvider(task, routing, summary, thinking Provider) (*RouterProvider, error) {
	if task == nil {
		return nil, fmt.Errorf("provider.router: task provider is required")
	}
	return &RouterProvider{
		task:     task,
		routing:  routing,  // nil → fallback to task at resolve time
		summary:  summary,  // nil → fallback to task at resolve time
		thinking: thinking, // nil → fallback to task at resolve time
	}, nil
}

// resolve picks the Provider tier that matches the hint in ctx.
// When the requested tier is not configured it falls back to task and emits a
// WARN so that silent degradation (especially thinking→task) is observable.
func (r *RouterProvider) resolve(ctx context.Context) Provider {
	warnFallback := func(hint ModelHint) Provider {
		slog.WarnContext(ctx, "router: requested tier not configured, falling back to task",
			"requested_hint", hint,
			"source", SourceFromContext(ctx),
		)
		return r.task
	}
	switch HintFromContext(ctx) {
	case ModelHintRouter:
		if r.routing != nil {
			return r.routing
		}
		return warnFallback(ModelHintRouter)
	case ModelHintSummary:
		if r.summary != nil {
			return r.summary
		}
		return warnFallback(ModelHintSummary)
	case ModelHintThinking:
		if r.thinking != nil {
			return r.thinking
		}
		return warnFallback(ModelHintThinking)
	default:
		return r.task
	}
}

// Complete routes the call to the appropriate tier and delegates.
func (r *RouterProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	return r.resolve(ctx).Complete(ctx, messages)
}

// CompleteWithTools routes the call to the appropriate tier and delegates.
func (r *RouterProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	return r.resolve(ctx).CompleteWithTools(ctx, messages, tools)
}
