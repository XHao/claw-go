package provider

import (
	"context"
	"errors"
	"strings"
)

// FallbackProvider retries with fallback provider when the primary model is unavailable.
type FallbackProvider struct {
	primary  Provider
	fallback Provider
}

// WrapFallback wraps primary with runtime fallback.
// If primary fails with model-unavailable style errors, fallback is attempted.
func WrapFallback(primary, fallback Provider) Provider {
	if primary == nil {
		return fallback
	}
	if fallback == nil {
		return primary
	}
	return &FallbackProvider{primary: primary, fallback: fallback}
}

func (f *FallbackProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	result, err := f.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

func (f *FallbackProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	result, err := f.primary.CompleteWithTools(ctx, messages, tools)
	if err == nil {
		return result, nil
	}
	if !shouldFallback(err) {
		return CompleteResult{}, err
	}
	return f.fallback.CompleteWithTools(ctx, messages, tools)
}

func shouldFallback(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "context canceled") || strings.Contains(msg, "deadline exceeded") {
		return false
	}
	for _, key := range []string{
		"model_not_found",
		"model not found",
		"does not exist",
		"not available",
		"unavailable",
		"temporarily overloaded",
		"status 404",
		"status 429",
		"status 500",
		"status 502",
		"status 503",
		"status 504",
	} {
		if strings.Contains(msg, key) {
			return true
		}
	}
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
