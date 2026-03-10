package provider

import (
	"context"
	"fmt"
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
// Any optional tier that is nil falls back silently to task.
type RouterProvider struct {
	routing  Provider // Tier 1 — nil → task
	task     Provider // Tier 2 (required)
	summary  Provider // Tier 2b — nil → task
	thinking Provider // Tier 3 — nil → task
}

// NewRouterProvider builds a RouterProvider from pre-constructed Provider
// instances.  Only task is required; pass nil for optional tiers to use task
// as a fallback.
func NewRouterProvider(task, routing, summary, thinking Provider) (*RouterProvider, error) {
	if task == nil {
		return nil, fmt.Errorf("provider.router: task provider is required")
	}
	r := &RouterProvider{task: task}
	if routing != nil {
		r.routing = routing
	} else {
		r.routing = task
	}
	if summary != nil {
		r.summary = summary
	} else {
		r.summary = task
	}
	if thinking != nil {
		r.thinking = thinking
	} else {
		r.thinking = task
	}
	return r, nil
}

// resolve picks the Provider tier that matches the hint in ctx.
func (r *RouterProvider) resolve(ctx context.Context) Provider {
	switch HintFromContext(ctx) {
	case ModelHintRouter:
		return r.routing
	case ModelHintSummary:
		return r.summary
	case ModelHintThinking:
		return r.thinking
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
