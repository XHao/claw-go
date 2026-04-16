package provider_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/XHao/claw-go/provider"
)

func TestClassify_NilError(t *testing.T) {
	if provider.Classify(nil) != nil {
		t.Error("expected nil for nil error")
	}
}

func TestClassify_Idempotent(t *testing.T) {
	orig := errors.New("some error")
	ce := &provider.ClassifiedError{
		Reason: provider.FailReasonUnknown,
		Cause:  orig,
	}
	got := provider.Classify(ce)
	if got != ce {
		t.Error("expected same pointer for already-classified error")
	}
}

func TestClassify_ContextCanceled(t *testing.T) {
	ce := provider.Classify(context.Canceled)
	if ce.ShouldFallback || ce.ShouldCompress || ce.Retryable {
		t.Errorf("context.Canceled should have all decision fields false, got %+v", ce)
	}
	if ce.UserMessage != "" {
		t.Errorf("expected empty UserMessage for context.Canceled, got %q", ce.UserMessage)
	}
}

func TestClassify_ContextDeadlineExceeded(t *testing.T) {
	ce := provider.Classify(context.DeadlineExceeded)
	if ce.ShouldFallback || ce.ShouldCompress || ce.Retryable {
		t.Errorf("context.DeadlineExceeded should have all decision fields false, got %+v", ce)
	}
	if ce.UserMessage != "" {
		t.Errorf("expected empty UserMessage for context.DeadlineExceeded, got %q", ce.UserMessage)
	}
}

func TestClassify_ContextOverflow(t *testing.T) {
	ce := provider.Classify(errors.New("context_length exceeded: 128000 tokens"))
	if ce.Reason != provider.FailReasonContextOverflow {
		t.Errorf("expected context_overflow, got %s", ce.Reason)
	}
	if !ce.ShouldCompress {
		t.Error("expected ShouldCompress=true")
	}
	if ce.ShouldFallback {
		t.Error("expected ShouldFallback=false for context overflow")
	}
}

func TestClassify_Auth(t *testing.T) {
	ce := provider.Classify(errors.New("status 401: invalid_api_key"))
	if ce.Reason != provider.FailReasonAuth {
		t.Errorf("expected auth, got %s", ce.Reason)
	}
	if !ce.ShouldFallback {
		t.Error("expected ShouldFallback=true")
	}
	if ce.Retryable {
		t.Error("expected Retryable=false for auth error")
	}
}

func TestClassify_RateLimit(t *testing.T) {
	ce := provider.Classify(errors.New("status 429: rate_limit exceeded"))
	if ce.Reason != provider.FailReasonRateLimit {
		t.Errorf("expected rate_limit, got %s", ce.Reason)
	}
	if !ce.ShouldFallback || !ce.Retryable {
		t.Errorf("expected ShouldFallback=true and Retryable=true, got %+v", ce)
	}
}

func TestClassify_ModelNotFound(t *testing.T) {
	ce := provider.Classify(errors.New("model_not_found: gpt-99"))
	if ce.Reason != provider.FailReasonModelNotFound {
		t.Errorf("expected model_not_found, got %s", ce.Reason)
	}
	if !ce.ShouldFallback {
		t.Error("expected ShouldFallback=true")
	}
	if ce.Retryable {
		t.Error("expected Retryable=false for model_not_found")
	}
}

func TestClassify_Overloaded(t *testing.T) {
	ce := provider.Classify(errors.New("status 503: temporarily overloaded"))
	if ce.Reason != provider.FailReasonOverloaded {
		t.Errorf("expected overloaded, got %s", ce.Reason)
	}
	if !ce.ShouldFallback || !ce.Retryable {
		t.Errorf("expected ShouldFallback=true and Retryable=true, got %+v", ce)
	}
}

func TestClassify_Timeout(t *testing.T) {
	ce := provider.Classify(&mockTimeoutErr{})
	if ce.Reason != provider.FailReasonTimeout {
		t.Errorf("expected timeout, got %s", ce.Reason)
	}
	if !ce.ShouldFallback || !ce.Retryable {
		t.Errorf("expected ShouldFallback=true and Retryable=true, got %+v", ce)
	}
}

func TestClassify_NoFalsePositiveAuthOnModelName(t *testing.T) {
	// "401" in a model name must not trigger auth classification.
	ce := provider.Classify(errors.New("error calling model-401-v2: connection refused"))
	if ce.Reason == provider.FailReasonAuth {
		t.Error("model name containing '401' should not classify as auth")
	}
}

func TestClassify_Unknown(t *testing.T) {
	ce := provider.Classify(errors.New("something completely unexpected"))
	if ce.Reason != provider.FailReasonUnknown {
		t.Errorf("expected unknown, got %s", ce.Reason)
	}
	if ce.ShouldFallback || ce.ShouldCompress || ce.Retryable {
		t.Errorf("expected all decision fields false for unknown, got %+v", ce)
	}
}

func TestClassify_UserMessages(t *testing.T) {
	cases := []struct {
		err    error
		reason provider.FailReason
	}{
		{errors.New("context_length exceeded"), provider.FailReasonContextOverflow},
		{errors.New("status 401: invalid_api_key"), provider.FailReasonAuth},
		{errors.New("status 429: rate_limit"), provider.FailReasonRateLimit},
		{errors.New("model_not_found"), provider.FailReasonModelNotFound},
		{errors.New("status 503: overloaded"), provider.FailReasonOverloaded},
		{&mockTimeoutErr{}, provider.FailReasonTimeout},
		{errors.New("unknown error xyz"), provider.FailReasonUnknown},
	}
	for _, tc := range cases {
		ce := provider.Classify(tc.err)
		if ce.Reason != tc.reason {
			t.Errorf("err=%q: expected reason %s, got %s", tc.err, tc.reason, ce.Reason)
		}
		if ce.UserMessage == "" {
			t.Errorf("err=%q: expected non-empty UserMessage", tc.err)
		}
	}
}

// mockTimeoutErr implements net.Error with Timeout()=true.
type mockTimeoutErr struct{}

func (m *mockTimeoutErr) Error() string   { return "mock timeout" }
func (m *mockTimeoutErr) Timeout() bool   { return true }
func (m *mockTimeoutErr) Temporary() bool { return true }

// ensure mockTimeoutErr satisfies net.Error at compile time
var _ net.Error = &mockTimeoutErr{}
