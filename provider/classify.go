package provider

import (
	"context"
	"errors"
	"net"
	"strings"
)

// FailReason describes the category of a provider error.
type FailReason string

const (
	FailReasonContextOverflow FailReason = "context_overflow"
	FailReasonRateLimit       FailReason = "rate_limit"
	FailReasonModelNotFound   FailReason = "model_not_found"
	FailReasonOverloaded      FailReason = "overloaded"
	FailReasonAuth            FailReason = "auth"
	FailReasonTimeout         FailReason = "timeout"
	FailReasonUnknown         FailReason = "unknown"
)

// ClassifiedError wraps a provider error with structured decision fields.
// Consumers read fields instead of parsing error strings.
type ClassifiedError struct {
	Reason         FailReason
	Retryable      bool   // worth retrying on the same provider
	ShouldFallback bool   // switch to fallback provider
	ShouldCompress bool   // compress history and retry (context_overflow only)
	UserMessage    string // localised message to show the user; "" = silent
	Cause          error
}

func (e *ClassifiedError) Error() string { return e.Cause.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Cause }

// Classify classifies err into a ClassifiedError.
// If err is already a *ClassifiedError it is returned as-is (idempotent).
// If err is nil, nil is returned.
func Classify(err error) *ClassifiedError {
	if err == nil {
		return nil
	}
	var ce *ClassifiedError
	if errors.As(err, &ce) {
		return ce
	}
	return classify(err)
}

func classify(err error) *ClassifiedError {
	// context cancellation / deadline — silent, highest priority
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &ClassifiedError{Reason: FailReasonUnknown, Cause: err}
	}

	msg := strings.ToLower(err.Error())

	// context overflow
	for _, kw := range []string{"context_length", "context_window", "maximum_context", "too_long", "context length"} {
		if strings.Contains(msg, kw) {
			return &ClassifiedError{
				Reason:         FailReasonContextOverflow,
				ShouldCompress: true,
				UserMessage:    "消息历史过长，正在压缩后重试…",
				Cause:          err,
			}
		}
	}

	// auth — use "status NNN" prefix to avoid matching model names or URL paths
	for _, kw := range []string{"status 401", "status 403", "invalid_api_key", "invalid api key", "authentication"} {
		if strings.Contains(msg, kw) {
			return &ClassifiedError{
				Reason:         FailReasonAuth,
				ShouldFallback: true,
				UserMessage:    "认证失败，请检查 API Key 配置",
				Cause:          err,
			}
		}
	}

	// rate limit
	for _, kw := range []string{"429", "rate_limit", "rate limit", "quota_exceeded", "quota exceeded", "too_many_requests", "too many requests"} {
		if strings.Contains(msg, kw) {
			return &ClassifiedError{
				Reason:         FailReasonRateLimit,
				Retryable:      true,
				ShouldFallback: true,
				UserMessage:    "请求过于频繁，稍后重试",
				Cause:          err,
			}
		}
	}

	// model not found
	for _, kw := range []string{"model_not_found", "model not found", "does not exist", "status 404"} {
		if strings.Contains(msg, kw) {
			return &ClassifiedError{
				Reason:         FailReasonModelNotFound,
				ShouldFallback: true,
				UserMessage:    "模型不可用，正在切换备用模型",
				Cause:          err,
			}
		}
	}

	// overloaded — avoid bare "unavailable" which matches unrelated infrastructure errors
	for _, kw := range []string{"overloaded", "temporarily overloaded", "status 500", "status 502", "status 503", "status 504"} {
		if strings.Contains(msg, kw) {
			return &ClassifiedError{
				Reason:         FailReasonOverloaded,
				Retryable:      true,
				ShouldFallback: true,
				UserMessage:    "模型过载，正在切换备用模型",
				Cause:          err,
			}
		}
	}

	// timeout via net.Error interface — re-check context signals in case a
	// non-standard HTTP client wraps context.DeadlineExceeded in a net.Error
	// without preserving the chain, so errors.Is above would have missed it.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return &ClassifiedError{Reason: FailReasonUnknown, Cause: err}
		}
		return &ClassifiedError{
			Reason:         FailReasonTimeout,
			Retryable:      true,
			ShouldFallback: true,
			UserMessage:    "请求超时，正在重试",
			Cause:          err,
		}
	}

	return &ClassifiedError{
		Reason:      FailReasonUnknown,
		UserMessage: "遇到未知错误，请重试",
		Cause:       err,
	}
}
