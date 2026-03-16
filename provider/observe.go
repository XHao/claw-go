package provider

import (
	"context"
	"time"
)

type usageObserverKey struct{}

// UsageEvent describes one provider call for realtime observers.
type UsageEvent struct {
	At               string
	Hint             string
	Source           string
	ModelKey         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	LatencyMs        int64
	StopReason       string
	IsError          bool
}

// UsageObserver receives a UsageEvent emitted after each provider call.
type UsageObserver func(UsageEvent)

// WithUsageObserver attaches an observer callback to ctx.
func WithUsageObserver(ctx context.Context, obs UsageObserver) context.Context {
	if obs == nil {
		return ctx
	}
	return context.WithValue(ctx, usageObserverKey{}, obs)
}

func usageObserverFromContext(ctx context.Context) UsageObserver {
	obs, _ := ctx.Value(usageObserverKey{}).(UsageObserver)
	return obs
}

// ObserveProvider emits usage telemetry to the context observer after each call.
type ObserveProvider struct {
	inner Provider
}

// WrapObserve wraps inner with usage-observer emission.
func WrapObserve(inner Provider) Provider {
	if inner == nil {
		return nil
	}
	return &ObserveProvider{inner: inner}
}

func (o *ObserveProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	result, err := o.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

func (o *ObserveProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	start := time.Now()
	result, err := o.inner.CompleteWithTools(ctx, messages, tools)
	if obs := usageObserverFromContext(ctx); obs != nil {
		hint := string(HintFromContext(ctx))
		if hint == "" {
			hint = string(ModelHintTask)
		}
		stopReason := result.StopReason
		if err != nil && stopReason == "" {
			stopReason = "error"
		}
		obs(UsageEvent{
			At:               start.UTC().Format(time.RFC3339),
			Hint:             hint,
			Source:           SourceFromContext(ctx),
			ModelKey:         result.Model.ModelKey,
			Model:            result.Model.Model,
			PromptTokens:     result.Usage.PromptTokens,
			CompletionTokens: result.Usage.CompletionTokens,
			TotalTokens:      result.Usage.TotalTokens,
			LatencyMs:        time.Since(start).Milliseconds(),
			StopReason:       stopReason,
			IsError:          err != nil,
		})
	}
	return result, err
}
