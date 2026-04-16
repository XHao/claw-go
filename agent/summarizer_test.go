package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/XHao/claw-go/provider"
)

// mockProvider implements provider.Provider for testing.
type mockProvider struct {
	completeFunc func(ctx context.Context, msgs []provider.Message) (string, error)
}

func (m *mockProvider) Complete(ctx context.Context, msgs []provider.Message) (string, error) {
	if m.completeFunc != nil {
		return m.completeFunc(ctx, msgs)
	}
	return "", nil
}

func (m *mockProvider) CompleteWithTools(ctx context.Context, msgs []provider.Message, tools []provider.ToolDef) (provider.CompleteResult, error) {
	return provider.CompleteResult{}, nil
}

func TestTurnSummarizerSuccess(t *testing.T) {
	p := &mockProvider{
		completeFunc: func(_ context.Context, msgs []provider.Message) (string, error) {
			return "- user asked to refactor\n- modified auth.go", nil
		},
	}
	s := newTurnSummarizer(p)
	if s == nil {
		t.Fatal("expected non-nil summarizer")
	}

	msgs := []provider.Message{
		{Role: "user", Content: "refactor the auth module"},
		{Role: "assistant", Content: "Sure, I'll start with auth.go"},
	}
	result, err := s.SummarizeTurn(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "- user asked to refactor\n- modified auth.go" {
		t.Errorf("unexpected result: %q", result)
	}
}

func TestTurnSummarizerProviderError(t *testing.T) {
	p := &mockProvider{
		completeFunc: func(_ context.Context, msgs []provider.Message) (string, error) {
			return "", errors.New("network error")
		},
	}
	s := newTurnSummarizer(p)
	_, err := s.SummarizeTurn(context.Background(), []provider.Message{
		{Role: "user", Content: "hello"},
	})
	if err == nil {
		t.Error("expected error from provider, got nil")
	}
}

func TestNewTurnSummarizerNilProvider(t *testing.T) {
	s := newTurnSummarizer(nil)
	if s != nil {
		t.Error("expected nil summarizer for nil provider")
	}
}
