package provider

import (
	"context"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

type usageObserverKey struct{}

// UsageEvent describes one provider call for realtime observers.
type UsageEvent struct {
	At               string
	Hint             string
	Source           string
	ModelKey         string
	Model            string
	InputTokensEst   int
	ContextTokensEst int
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
		inputEst, contextEst := estimatePromptBreakdown(messages, result.Usage.PromptTokens)
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
			InputTokensEst:   inputEst,
			ContextTokensEst: contextEst,
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

func estimatePromptBreakdown(messages []Message, promptTokens int) (inputTokensEst, contextTokensEst int) {
	if promptTokens <= 0 {
		return 0, 0
	}
	totalLen := 0
	lastUserLen := 0
	for _, m := range messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		totalLen += utf8.RuneCountInString(content)
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		content := strings.TrimSpace(messages[i].Content)
		if content == "" {
			continue
		}
		lastUserLen = utf8.RuneCountInString(content)
		break
	}
	if totalLen <= 0 || lastUserLen <= 0 {
		return 0, promptTokens
	}
	inputTokensEst = int(math.Round(float64(promptTokens) * float64(lastUserLen) / float64(totalLen)))
	if inputTokensEst < 0 {
		inputTokensEst = 0
	}
	if inputTokensEst > promptTokens {
		inputTokensEst = promptTokens
	}
	contextTokensEst = promptTokens - inputTokensEst
	return inputTokensEst, contextTokensEst
}
