package agent

import (
	"context"
	"strings"

	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
)

const turnSummarySystemPrompt = `You are a conversation summarizer. Summarize the following conversation turn in 3-5 concise bullet points.
Focus on: what the user asked, what was done, key decisions or findings, any files modified.
Be factual. Output plain text bullets only, no markdown headers.`

// turnSummarizer implements session.TurnSummarizer using a provider.Provider.
type turnSummarizer struct {
	provider provider.Provider
}

// newTurnSummarizer returns a TurnSummarizer backed by p, or nil if p is nil.
// Callers should treat a nil return as "summarization disabled".
func newTurnSummarizer(p provider.Provider) session.TurnSummarizer {
	if p == nil {
		return nil
	}
	return &turnSummarizer{provider: p}
}

// NewTurnSummarizer is the exported version of newTurnSummarizer.
// Used by cmd/claw/main.go to inject the summarizer into the session store.
func NewTurnSummarizer(p provider.Provider) session.TurnSummarizer {
	return newTurnSummarizer(p)
}

// SummarizeTurn calls the provider with a fixed summarization prompt and returns
// the result. Uses ModelHintSummary so RouterProvider picks the summary-tier model.
func (s *turnSummarizer) SummarizeTurn(ctx context.Context, messages []provider.Message) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	input := make([]provider.Message, 0, len(messages)+1)
	input = append(input, provider.Message{Role: "system", Content: turnSummarySystemPrompt})
	input = append(input, messages...)

	summaryCtx := provider.WithModelHint(ctx, provider.ModelHintSummary)
	result, err := s.provider.Complete(summaryCtx, input)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result), nil
}
