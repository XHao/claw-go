package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OpenAIProvider implements Provider against the OpenAI Chat Completions API
// (or any compatible endpoint such as Ollama, Azure, local proxies).
type OpenAIProvider struct {
	baseURL        string
	apiKey         string
	model          string
	maxTokens      int
	thinkingBudget int
	streamEnabled  bool
	httpClient     *http.Client

	// Negotiated capabilities — learned from API responses at runtime.
	// Once a capability is probed, the result is cached so subsequent calls
	// don't pay the retry cost.
	caps   negotiatedCaps
	capsMu sync.RWMutex
}

// negotiatedCaps tracks which optional features the upstream API actually supports.
// Fields are tri-state: nil = not probed yet, true/false = probed result.
type negotiatedCaps struct {
	streamOptions *bool // supports stream_options.include_usage
	thinking      *bool // supports thinking.budget_tokens
}

// NewOpenAI creates an OpenAIProvider.
func NewOpenAI(baseURL, apiKey, model string, maxTokens, timeoutSeconds, thinkingBudget int, streamEnabled bool) *OpenAIProvider {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	return &OpenAIProvider{
		baseURL:        baseURL,
		apiKey:         apiKey,
		model:          model,
		maxTokens:      maxTokens,
		thinkingBudget: thinkingBudget,
		streamEnabled:  streamEnabled,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutSeconds) * time.Second,
		},
	}
}

// ----- wire types -----

type oaMessage struct {
	Role       string            `json:"role"`
	Content    string            `json:"content,omitempty"`
	ToolCalls  []ToolCallRequest `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Name       string            `json:"name,omitempty"`
}

type oaToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type oaTool struct {
	Type     string         `json:"type"` // "function"
	Function oaToolFunction `json:"function"`
}

type oaRequest struct {
	Model         string           `json:"model"`
	Messages      []oaMessage      `json:"messages"`
	MaxTokens     int              `json:"max_tokens,omitempty"`
	Stream        bool             `json:"stream"`
	StreamOptions *oaStreamOptions `json:"stream_options,omitempty"`
	Tools         []oaTool         `json:"tools,omitempty"`
	ToolChoice    string           `json:"tool_choice,omitempty"`
	Thinking      *oaThinking      `json:"thinking,omitempty"`
}

// oaStreamOptions requests usage stats in the final SSE chunk.
type oaStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// oaThinking is the Anthropic-style extended thinking parameter.
// Sent as {"type":"enabled","budget_tokens":N} when the model supports thinking.
type oaThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type oaResponse struct {
	Choices []struct {
		Message struct {
			Content   string            `json:"content"`
			ToolCalls []ToolCallRequest `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// ── SSE streaming wire types ────────────────────────────────────────────────

type oaStreamChunk struct {
	Choices []oaStreamChoice `json:"choices"`
	Usage   *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

type oaStreamChoice struct {
	Delta        oaStreamDelta `json:"delta"`
	FinishReason *string       `json:"finish_reason"`
}

type oaStreamDelta struct {
	Content   string              `json:"content,omitempty"`
	ToolCalls []oaStreamToolDelta `json:"tool_calls,omitempty"`
}

// oaStreamToolDelta carries incremental tool call fragments.
// The first chunk for a given index carries ID+Name; subsequent chunks only
// carry argument fragments.
type oaStreamToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// Complete sends the conversation history to the model and returns the reply.
func (p *OpenAIProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	result, err := p.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// CompleteWithTools calls the LLM with optional tool definitions. It returns
// either a final text reply or a list of tool call requests.
//
// Feature negotiation: on the first call, optional capabilities (stream_options,
// thinking) are sent optimistically. If the upstream API rejects them (HTTP 400
// with a recognisable error), the offending field is stripped and the request is
// retried automatically. The result is cached so subsequent calls skip the probe.
func (p *OpenAIProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	// Build wire messages.
	oaMsgs := make([]oaMessage, len(messages))
	for i, m := range messages {
		content := m.Content
		if m.Role == "tool" && content == "" {
			content = "(no output)"
		}
		oaMsgs[i] = oaMessage{
			Role:       m.Role,
			Content:    content,
			ToolCalls:  m.ToolCalls,
			ToolCallID: m.ToolCallID,
			Name:       m.Name,
		}
	}

	// Build tool list.
	var oaTools []oaTool
	for _, t := range tools {
		params := t.Parameters
		if params == nil {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		oaTools = append(oaTools, oaTool{
			Type: "function",
			Function: oaToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}

	// Determine streaming callback (if any).
	var streamFn StreamFunc
	if p.streamEnabled {
		streamFn = streamFuncFromContext(ctx)
	}

	req := p.buildRequest(oaMsgs, oaTools, streamFn != nil)
	return p.doWithNegotiation(ctx, req, streamFn)
}

// buildRequest constructs an oaRequest with optional features based on cached
// negotiation results.
func (p *OpenAIProvider) buildRequest(msgs []oaMessage, tools []oaTool, wantStream bool) oaRequest {
	p.capsMu.RLock()
	caps := p.caps
	p.capsMu.RUnlock()

	req := oaRequest{
		Model:     p.model,
		Messages:  msgs,
		MaxTokens: p.maxTokens,
		Stream:    wantStream,
		Tools:     tools,
	}
	if len(tools) > 0 {
		req.ToolChoice = "auto"
	}

	// Thinking: send if configured and not known-unsupported.
	if p.thinkingBudget > 0 && (caps.thinking == nil || *caps.thinking) {
		req.Thinking = &oaThinking{
			Type:         "enabled",
			BudgetTokens: p.thinkingBudget,
		}
	}

	// StreamOptions: send if streaming and not known-unsupported.
	if wantStream && (caps.streamOptions == nil || *caps.streamOptions) {
		req.StreamOptions = &oaStreamOptions{IncludeUsage: true}
	}

	return req
}

// doWithNegotiation sends the request and, on a 400 error that mentions an
// unsupported feature, strips that feature, caches the result, and retries once.
func (p *OpenAIProvider) doWithNegotiation(ctx context.Context, req oaRequest, streamFn StreamFunc) (CompleteResult, error) {
	result, err := p.doHTTP(ctx, req, streamFn)
	if err == nil {
		// First successful call: confirm any probed capabilities.
		p.confirmCaps(req)
		return result, nil
	}

	// Check if the error is a 400 that we can negotiate around.
	stripped, field := p.tryStripRejectedFeature(err, &req)
	if !stripped {
		return CompleteResult{}, err
	}

	slog.WarnContext(ctx, "openai: upstream rejected optional feature, retrying without it",
		"feature", field, "model", p.model, "base_url", p.baseURL)

	result, err = p.doHTTP(ctx, req, streamFn)
	if err == nil {
		p.confirmCaps(req)
	}
	return result, err
}

// doHTTP marshals the request, sends it, and parses the response.
// Stream vs non-stream is decided by Content-Type auto-detection.
func (p *OpenAIProvider) doHTTP(ctx context.Context, req oaRequest, streamFn StreamFunc) (CompleteResult, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("openai: marshal: %w", err)
	}

	url := p.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return CompleteResult{}, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("openai: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		rawBody, _ := io.ReadAll(resp.Body)
		return CompleteResult{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(rawBody))
	}

	// ── Auto-detect: Content-Type tells us the actual response format ────
	ct := resp.Header.Get("Content-Type")
	isSSE := strings.Contains(ct, "text/event-stream")

	if streamFn != nil && isSSE {
		return p.readSSE(resp.Body, streamFn)
	}

	// Non-streaming (or server ignored stream:true and returned JSON).
	return p.readJSON(resp.Body)
}

// readJSON parses a standard (non-streaming) Chat Completions JSON response.
func (p *OpenAIProvider) readJSON(body io.Reader) (CompleteResult, error) {
	rawBody, err := io.ReadAll(body)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("openai: read body: %w", err)
	}
	var result oaResponse
	if err := json.Unmarshal(rawBody, &result); err != nil {
		return CompleteResult{}, fmt.Errorf("openai: decode: %w", err)
	}
	if result.Error != nil {
		return CompleteResult{}, fmt.Errorf("openai: api error %s: %s", result.Error.Type, result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return CompleteResult{}, fmt.Errorf("openai: no choices returned")
	}
	usage := Usage{
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens:      result.Usage.TotalTokens,
	}
	choice := result.Choices[0]
	if len(choice.Message.ToolCalls) > 0 {
		return CompleteResult{
			ToolCalls:  choice.Message.ToolCalls,
			StopReason: choice.FinishReason,
			Usage:      usage,
			Model:      ModelMeta{Model: p.model},
		}, nil
	}
	content := choice.Message.Content
	if content == "" {
		content = "(done)"
	}
	return CompleteResult{
		Content:    content,
		StopReason: choice.FinishReason,
		Usage:      usage,
		Model:      ModelMeta{Model: p.model},
	}, nil
}

// ── Capability negotiation helpers ──────────────────────────────────────────

// tryStripRejectedFeature inspects a 400-class error message and, if it
// matches a known optional feature, strips that feature from req, caches the
// negative result, and returns true.
func (p *OpenAIProvider) tryStripRejectedFeature(err error, req *oaRequest) (stripped bool, field string) {
	msg := err.Error()
	// Only negotiate on HTTP 400 (Bad Request).
	if !strings.Contains(msg, "status 400") {
		return false, ""
	}
	lower := strings.ToLower(msg)

	// stream_options rejected?
	if req.StreamOptions != nil && (strings.Contains(lower, "stream_options") || strings.Contains(lower, "stream_option")) {
		req.StreamOptions = nil
		p.setCap(func(c *negotiatedCaps) { f := false; c.streamOptions = &f })
		return true, "stream_options"
	}

	// thinking rejected?
	if req.Thinking != nil && (strings.Contains(lower, "thinking") || strings.Contains(lower, "budget_tokens")) {
		req.Thinking = nil
		p.setCap(func(c *negotiatedCaps) { f := false; c.thinking = &f })
		return true, "thinking"
	}

	return false, ""
}

// confirmCaps marks features that were present in a successful request as
// supported — but only if they haven't already been cached (avoid overwriting
// a cached "false" with "true" when the feature was already stripped).
func (p *OpenAIProvider) confirmCaps(req oaRequest) {
	p.capsMu.Lock()
	defer p.capsMu.Unlock()
	if req.StreamOptions != nil && p.caps.streamOptions == nil {
		t := true
		p.caps.streamOptions = &t
	}
	if req.Thinking != nil && p.caps.thinking == nil {
		t := true
		p.caps.thinking = &t
	}
}

func (p *OpenAIProvider) setCap(fn func(*negotiatedCaps)) {
	p.capsMu.Lock()
	fn(&p.caps)
	p.capsMu.Unlock()
}

// readSSE consumes an SSE stream from the OpenAI Chat Completions API,
// calls streamFn for each text delta, accumulates tool calls, and returns
// the final CompleteResult once the stream ends.
func (p *OpenAIProvider) readSSE(body io.Reader, streamFn StreamFunc) (CompleteResult, error) {
	scanner := bufio.NewScanner(body)

	var (
		contentBuf   strings.Builder
		toolCalls    []ToolCallRequest // accumulated tool calls by index
		finishReason string
		usage        Usage
	)

	for scanner.Scan() {
		line := scanner.Text()

		// SSE blank lines separate events — skip.
		if line == "" {
			continue
		}
		// SSE comments start with ':' — skip.
		if strings.HasPrefix(line, ":") {
			continue
		}
		// Strip "data: " prefix.
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		// Terminal sentinel.
		if data == "[DONE]" {
			break
		}

		var chunk oaStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// Skip malformed chunks rather than aborting the entire stream.
			continue
		}

		if chunk.Error != nil {
			return CompleteResult{}, fmt.Errorf("openai: stream error %s: %s", chunk.Error.Type, chunk.Error.Message)
		}

		// Capture usage from the final chunk (when stream_options.include_usage is set).
		if chunk.Usage != nil {
			usage = Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
		}

		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if chunk.Choices[0].FinishReason != nil {
			finishReason = *chunk.Choices[0].FinishReason
		}

		// ── Text content delta ──────────────────────────────────────────
		if delta.Content != "" {
			contentBuf.WriteString(delta.Content)
			streamFn(delta.Content)
		}

		// ── Tool call deltas (incremental) ──────────────────────────────
		for _, tcd := range delta.ToolCalls {
			// Grow the accumulated slice if needed.
			for len(toolCalls) <= tcd.Index {
				toolCalls = append(toolCalls, ToolCallRequest{
					Type: "function",
				})
			}
			tc := &toolCalls[tcd.Index]
			if tcd.ID != "" {
				tc.ID = tcd.ID
			}
			if tcd.Function.Name != "" {
				tc.Function.Name = tcd.Function.Name
			}
			tc.Function.Arguments += tcd.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return CompleteResult{}, fmt.Errorf("openai: stream read: %w", err)
	}

	// Build final result — same shape as the non-streaming path.
	if len(toolCalls) > 0 {
		return CompleteResult{
			ToolCalls:  toolCalls,
			StopReason: finishReason,
			Usage:      usage,
			Model:      ModelMeta{Model: p.model},
		}, nil
	}

	content := contentBuf.String()
	if content == "" {
		content = "(done)"
	}
	return CompleteResult{
		Content:    content,
		StopReason: finishReason,
		Usage:      usage,
		Model:      ModelMeta{Model: p.model},
	}, nil
}

// ProbeCapabilities sends a minimal request to the upstream API to discover
// which optional features (streaming, stream_options, thinking) are supported.
// Results are cached in the provider's negotiatedCaps so that real requests
// never pay the probe/retry cost.
//
// The probe uses max_tokens=1 to minimise token spend.  It is safe to call
// concurrently from multiple goroutines (the result writes are mutex-protected).
func (p *OpenAIProvider) ProbeCapabilities(ctx context.Context) {
	p.capsMu.RLock()
	allProbed := p.caps.streamOptions != nil && (p.thinkingBudget == 0 || p.caps.thinking != nil)
	p.capsMu.RUnlock()
	if allProbed {
		return // nothing to probe
	}

	// Minimal probe payload: single user message, max_tokens=1.
	msgs := []oaMessage{{Role: "user", Content: "hi"}}

	// Build request with all optional features the config asks for.
	req := p.buildRequest(msgs, nil, p.streamEnabled)
	req.MaxTokens = 1

	// Use a short timeout — probes shouldn't block startup for long.
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// Send and interpret — we don't care about the LLM output, only whether
	// the API accepted or rejected the optional fields.
	_, err := p.doHTTP(probeCtx, req, func(string) {})
	if err == nil {
		p.confirmCaps(req)
		slog.InfoContext(ctx, "openai: probe ok",
			"model", p.model, "stream_options", capStr(p.caps.streamOptions), "thinking", capStr(p.caps.thinking))
		return
	}

	// Try stripping rejected features one at a time and re-probe.
	for {
		stripped, field := p.tryStripRejectedFeature(err, &req)
		if !stripped {
			// Non-negotiable error — log and give up; real requests will
			// still negotiate on their own.
			slog.WarnContext(ctx, "openai: probe failed (non-negotiable)",
				"model", p.model, "err", err)
			return
		}
		slog.InfoContext(ctx, "openai: probe — feature unsupported",
			"model", p.model, "feature", field)

		probeCtx2, cancel2 := context.WithTimeout(ctx, 8*time.Second)
		_, err = p.doHTTP(probeCtx2, req, func(string) {})
		cancel2()
		if err == nil {
			p.confirmCaps(req)
			slog.InfoContext(ctx, "openai: probe ok (after negotiation)",
				"model", p.model, "stream_options", capStr(p.caps.streamOptions), "thinking", capStr(p.caps.thinking))
			return
		}
		// Loop: maybe a second feature also needs stripping.
	}
}

func capStr(b *bool) string {
	if b == nil {
		return "unknown"
	}
	if *b {
		return "yes"
	}
	return "no"
}
