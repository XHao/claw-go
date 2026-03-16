package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// MetricsRecord captures one LLM call event in a structured form.
// Written as a JSONL line to the metrics file.
type MetricsRecord struct {
	At               string `json:"at"`               // RFC3339Nano timestamp
	Hint             string `json:"hint"`             // ModelHint tier ("task", "router", …)
	Source           string `json:"source,omitempty"` // call site label, e.g. "agent/loop[i=0]"
	ModelKey         string `json:"model_key,omitempty"`
	Model            string `json:"model,omitempty"`
	PromptTokens     int    `json:"prompt_tokens"`     // tokens in the request
	CompletionTokens int    `json:"completion_tokens"` // tokens in the response
	TotalTokens      int    `json:"total_tokens"`      // prompt + completion
	LatencyMs        int64  `json:"latency_ms"`        // wall-clock ms
	StopReason       string `json:"stop_reason"`       // "stop", "tool_calls", …
	IsError          bool   `json:"is_error,omitempty"`
}

// MetricsProvider wraps any Provider and appends a MetricsRecord JSONL line
// to filePath for every LLM call.  It reads the ModelHint from ctx so the
// tier label appears in every record even when wrapping a RouterProvider.
//
// Wrap order (outermost first):
//
//	WrapMetrics( WrapDebug( RouterProvider(...) ) )
//
// Disable by not calling WrapMetrics (file path empty in config).
type MetricsProvider struct {
	inner Provider
	mu    sync.Mutex
	f     *os.File
}

// WrapMetrics wraps inner with JSONL metrics logging to filePath.
// Returns inner unchanged (with a stderr warning) if the file cannot be opened.
func WrapMetrics(inner Provider, filePath string) Provider {
	_ = os.MkdirAll(filepath.Dir(filePath), 0o700)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "metrics provider: cannot open %q: %v\n", filePath, err)
		return inner
	}
	return &MetricsProvider{inner: inner, f: f}
}

// Complete implements Provider.
func (m *MetricsProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	result, err := m.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// CompleteWithTools implements Provider; records the call to the metrics file.
func (m *MetricsProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	hint := string(HintFromContext(ctx))
	if hint == "" {
		hint = string(ModelHintTask)
	}

	start := time.Now()
	result, err := m.inner.CompleteWithTools(ctx, messages, tools)
	elapsed := time.Since(start)

	rec := MetricsRecord{
		At:               start.UTC().Format(time.RFC3339),
		Hint:             hint,
		Source:           SourceFromContext(ctx),
		ModelKey:         result.Model.ModelKey,
		Model:            result.Model.Model,
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens:      result.Usage.TotalTokens,
		LatencyMs:        elapsed.Milliseconds(),
		StopReason:       result.StopReason,
		IsError:          err != nil,
	}

	line, jerr := json.Marshal(rec)
	if jerr == nil {
		m.mu.Lock()
		_, _ = m.f.Write(append(line, '\n'))
		m.mu.Unlock()
	}

	return result, err
}
