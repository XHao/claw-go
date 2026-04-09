package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	anthropicDefaultBaseURL = "https://api.anthropic.com/v1"
	anthropicAPIVersion     = "2023-06-01"
)

// AnthropicProvider implements Provider using the Anthropic Messages API.
// It converts the OpenAI-style provider.Message format to Anthropic wire format
// internally, so the rest of the codebase remains unchanged.
type AnthropicProvider struct {
	baseURL        string
	apiKey         string
	model          string
	maxTokens      int
	thinkingBudget int // 0 = disabled
	streamEnabled  bool
	headers        map[string]string
	extraBody      map[string]any
	httpClient     *http.Client
}

// NewAnthropic creates an AnthropicProvider.
// baseURL overrides the default https://api.anthropic.com/v1 (useful for proxies).
// headers are extra HTTP headers sent with every request (e.g. X-Working-Dir for mcli).
// thinkingBudget > 0 enables extended thinking with that token budget.
// streamEnabled controls whether SSE streaming is used.
// extraBody keys are merged into the top-level JSON request body, allowing
// vendor-specific parameters to be passed through without code changes.
func NewAnthropic(baseURL, apiKey, model string, maxTokens, timeoutSeconds, thinkingBudget int, streamEnabled bool, headers map[string]string, extraBody map[string]any) *AnthropicProvider {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	return &AnthropicProvider{
		baseURL:        baseURL,
		apiKey:         apiKey,
		model:          model,
		maxTokens:      maxTokens,
		thinkingBudget: thinkingBudget,
		streamEnabled:  streamEnabled,
		headers:        headers,
		extraBody:      extraBody,
		httpClient:     &http.Client{Timeout: time.Duration(timeoutSeconds) * time.Second},
	}
}

// Complete implements Provider (text-only, no tools).
func (p *AnthropicProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	result, err := p.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// CompleteWithTools implements Provider.
func (p *AnthropicProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	var streamFn StreamFunc
	if p.streamEnabled {
		streamFn = streamFuncFromContext(ctx)
	}
	req := p.buildRequest(messages, tools, streamFn != nil)
	return p.doHTTP(ctx, req, streamFn)
}

// extractSystem separates system-role messages from the rest.
// Multiple system messages are joined with double newlines into a single string.
// This matches Anthropic's requirement: system is a top-level field, not a message.
func extractSystem(messages []Message) (system string, rest []Message) {
	var sysParts []string
	for _, m := range messages {
		if m.Role == "system" {
			if strings.TrimSpace(m.Content) != "" {
				sysParts = append(sysParts, strings.TrimSpace(m.Content))
			}
		} else {
			rest = append(rest, m)
		}
	}
	system = strings.Join(sysParts, "\n\n")
	return system, rest
}

// ── Anthropic wire types ──────────────────────────────────────────────────────

// antMessage is an Anthropic API message (request wire format).
type antMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string | []antContentBlock
}

// antContentBlock is a single content block inside an Anthropic message.
// Used for text, tool_use, tool_result, thinking, and image blocks.
type antContentBlock struct {
	Type string `json:"type"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// type=image
	Source *antImageSource `json:"source,omitempty"`
}

// antImageSource is the source descriptor for an image content block.
// Type "base64" is used now; "file" (with FileID) is the Files API upgrade path.
type antImageSource struct {
	Type      string `json:"type"`                 // "base64" | "file"
	MediaType string `json:"media_type,omitempty"` // required for type=base64
	Data      string `json:"data,omitempty"`       // base64-encoded bytes, for type=base64
	FileID    string `json:"file_id,omitempty"`    // for type=file (future Files API)
}

// antTool is the Anthropic tool definition wire format.
type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// antThinking is the extended thinking parameter.
type antThinking struct {
	Type         string `json:"type"` // always "enabled"
	BudgetTokens int    `json:"budget_tokens"`
}

// antRequest is the Anthropic Messages API request body.
type antRequest struct {
	Model     string       `json:"model"`
	System    string       `json:"system,omitempty"`
	Messages  []antMessage `json:"messages"`
	MaxTokens int          `json:"max_tokens"`
	Stream    bool         `json:"stream"`
	Tools     []antTool    `json:"tools,omitempty"`
	Thinking  *antThinking `json:"thinking,omitempty"`
}

// convertTools converts []provider.ToolDef to []antTool (Anthropic format).
// OpenAI uses "parameters" (JSON Schema); Anthropic uses "input_schema".
func convertTools(tools []ToolDef) []antTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]antTool, 0, len(tools))
	for _, t := range tools {
		schema := t.Parameters
		if schema == nil {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, antTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

// buildRequest assembles the full antRequest from provider.Messages and tools.
func (p *AnthropicProvider) buildRequest(messages []Message, tools []ToolDef, wantStream bool) antRequest {
	system, rest := extractSystem(messages)
	antMsgs := convertMessages(rest)
	antTools := convertTools(tools)

	req := antRequest{
		Model:     p.model,
		System:    system,
		Messages:  antMsgs,
		MaxTokens: p.maxTokens,
		Stream:    wantStream,
		Tools:     antTools,
	}
	if p.thinkingBudget > 0 && p.extraBody["thinking"] == nil {
		req.Thinking = &antThinking{
			Type:         "enabled",
			BudgetTokens: p.thinkingBudget,
		}
	}
	return req
}

// convertMessages converts []provider.Message (OpenAI format) to []antMessage
// (Anthropic wire format). System messages must be extracted before calling this.
func convertMessages(messages []Message) []antMessage {
	out := make([]antMessage, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case "assistant":
			var blocks []antContentBlock
			if m.Content != "" {
				blocks = append(blocks, antContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				var input json.RawMessage
				if tc.Function.Arguments != "" {
					input = json.RawMessage(tc.Function.Arguments)
				} else {
					input = json.RawMessage("{}")
				}
				blocks = append(blocks, antContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: input,
				})
			}
			if len(blocks) == 0 {
				blocks = []antContentBlock{{Type: "text", Text: "(done)"}}
			}
			out = append(out, antMessage{Role: "assistant", Content: blocks})

		case "tool":
			block := antContentBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			if m.Content == "" {
				block.Content = "(no output)"
			}
			out = append(out, antMessage{Role: "user", Content: []antContentBlock{block}})

		default: // "user"
			content := m.Content
			if content == "" && len(m.ImagePaths) == 0 {
				content = "(empty)"
			}

			if len(m.ImagePaths) > 0 {
				// Multi-part message: image blocks first, then text.
				var blocks []antContentBlock
				for _, imgPath := range m.ImagePaths {
					block, err := imageBlockFromFile(imgPath)
					if err != nil {
						// Single image failure is non-fatal — skip this image.
						slog.Warn("anthropic: failed to encode image, skipping", "path", imgPath, "err", err)
						continue
					}
					blocks = append(blocks, block)
				}
				if content != "" {
					blocks = append(blocks, antContentBlock{Type: "text", Text: content})
				}
				if len(blocks) == 0 {
					// All images failed and no text — send placeholder.
					blocks = []antContentBlock{{Type: "text", Text: "(empty)"}}
				}
				out = append(out, antMessage{Role: "user", Content: blocks})
			} else {
				out = append(out, antMessage{
					Role:    m.Role,
					Content: []antContentBlock{{Type: "text", Text: content}},
				})
			}
		}
	}
	return out
}

// ── Anthropic response wire types ─────────────────────────────────────────────

// antResponse is the non-streaming Anthropic Messages API response.
type antResponse struct {
	Type       string         `json:"type"`
	Content    []antRespBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      antUsage       `json:"usage"`
	Error      *antErrorBody  `json:"error,omitempty"`
}

// antRespBlock is one content block in the response.
type antRespBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Thinking string          `json:"thinking,omitempty"`
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

// antUsage holds token counts from the Anthropic response.
type antUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// antErrorBody is the error detail inside an Anthropic error response.
type antErrorBody struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// readJSON parses a non-streaming Anthropic Messages API response.
// Thinking blocks are silently filtered; only text and tool_use blocks are used.
func (p *AnthropicProvider) readJSON(body io.Reader) (CompleteResult, error) {
	raw, err := io.ReadAll(body)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("anthropic: read body: %w", err)
	}
	var resp antResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return CompleteResult{}, fmt.Errorf("anthropic: decode response: %w", err)
	}
	if resp.Error != nil {
		return CompleteResult{}, fmt.Errorf("anthropic: api error %s: %s", resp.Error.Type, resp.Error.Message)
	}

	usage := Usage{
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}

	var toolCalls []ToolCallRequest
	var textParts []string

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			args := string(block.Input)
			if args == "" || args == "null" {
				args = "{}"
			}
			toolCalls = append(toolCalls, ToolCallRequest{
				ID:   block.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: block.Name, Arguments: args},
			})
		case "thinking":
			// Silently discard — thinking content is internal model reasoning.
		}
	}

	if len(toolCalls) > 0 {
		return CompleteResult{
			ToolCalls:  toolCalls,
			StopReason: resp.StopReason,
			Usage:      usage,
			Model:      ModelMeta{Model: p.model},
		}, nil
	}

	content := strings.Join(textParts, "")
	if content == "" {
		content = "(done)"
	}
	return CompleteResult{
		Content:    content,
		StopReason: resp.StopReason,
		Usage:      usage,
		Model:      ModelMeta{Model: p.model},
	}, nil
}

// doHTTP marshals req, sends it to the Anthropic API, and dispatches to
// readJSON or readSSE depending on whether streaming was requested.
func (p *AnthropicProvider) doHTTP(ctx context.Context, req antRequest, streamFn StreamFunc) (CompleteResult, error) {
	body, err := marshalWithExtra(req, p.extraBody)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("anthropic: marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return CompleteResult{}, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
	for k, v := range p.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("anthropic: http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return CompleteResult{}, fmt.Errorf("anthropic: status %d: %s", resp.StatusCode, raw)
	}

	ct := resp.Header.Get("Content-Type")
	if streamFn != nil && strings.Contains(ct, "text/event-stream") {
		return p.readSSE(resp.Body, streamFn)
	}
	return p.readJSON(resp.Body)
}

// ── SSE streaming wire types ──────────────────────────────────────────────────

type antSSEEvent struct {
	Type string `json:"type"`

	// message_start
	Message *struct {
		Usage antUsage `json:"usage"`
	} `json:"message,omitempty"`

	// content_block_start
	Index        int `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block,omitempty"`

	// content_block_delta
	Delta *antSSEDelta `json:"delta,omitempty"`

	// message_delta
	Usage *antUsage `json:"usage,omitempty"`
}

type antSSEDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// sseBlock tracks one in-progress content block during streaming.
type sseBlock struct {
	blockType string // "text" | "tool_use" | "thinking"
	id        string // tool_use id
	name      string // tool_use name
	textBuf   strings.Builder
	argsBuf   strings.Builder
}

// readSSE consumes an Anthropic SSE stream, calls streamFn for each text delta,
// silently discards thinking deltas, and accumulates tool_use input.
func (p *AnthropicProvider) readSSE(body io.Reader, streamFn StreamFunc) (CompleteResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1*1024*1024) // 1 MB max line

	blocks := map[int]*sseBlock{}
	var inputTokens, outputTokens int
	var stopReason string

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "event:") {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		var ev antSSEEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // skip malformed
		}

		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				inputTokens = ev.Message.Usage.InputTokens
			}

		case "content_block_start":
			b := &sseBlock{}
			if ev.ContentBlock != nil {
				b.blockType = ev.ContentBlock.Type
				b.id = ev.ContentBlock.ID
				b.name = ev.ContentBlock.Name
			}
			blocks[ev.Index] = b

		case "content_block_delta":
			b := blocks[ev.Index]
			if b == nil || ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if b.blockType == "text" && ev.Delta.Text != "" {
					b.textBuf.WriteString(ev.Delta.Text)
					if streamFn != nil {
						streamFn(ev.Delta.Text)
					}
				}
			case "thinking_delta":
				// Silently discard — do NOT call streamFn.
			case "input_json_delta":
				if b.blockType == "tool_use" {
					b.argsBuf.WriteString(ev.Delta.PartialJSON)
				}
			}

		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				outputTokens = ev.Usage.OutputTokens
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return CompleteResult{}, fmt.Errorf("anthropic: stream read: %w", err)
	}

	usage := Usage{
		PromptTokens:     inputTokens,
		CompletionTokens: outputTokens,
		TotalTokens:      inputTokens + outputTokens,
	}

	// Build result from accumulated blocks in index order.
	indices := make([]int, 0, len(blocks))
	for idx := range blocks {
		indices = append(indices, idx)
	}
	// Sort indices for deterministic output.
	sort.Ints(indices)

	var toolCalls []ToolCallRequest
	var textParts []string

	for _, idx := range indices {
		b := blocks[idx]
		switch b.blockType {
		case "text":
			if t := b.textBuf.String(); t != "" {
				textParts = append(textParts, t)
			}
		case "tool_use":
			args := b.argsBuf.String()
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, ToolCallRequest{
				ID:   b.id,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: b.name, Arguments: args},
			})
		case "thinking":
			// Discard.
		}
	}

	if len(toolCalls) > 0 {
		return CompleteResult{
			ToolCalls:  toolCalls,
			StopReason: stopReason,
			Usage:      usage,
			Model:      ModelMeta{Model: p.model},
		}, nil
	}

	content := strings.Join(textParts, "")
	if content == "" {
		content = "(done)"
	}
	return CompleteResult{
		Content:    content,
		StopReason: stopReason,
		Usage:      usage,
		Model:      ModelMeta{Model: p.model},
	}, nil
}

// imageBlockFromFile reads a local image file and returns an Anthropic image
// content block with base64-encoded data.
// This is the current implementation; swap to Files API upload here in future.
func imageBlockFromFile(path string) (antContentBlock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return antContentBlock{}, fmt.Errorf("read image %q: %w", path, err)
	}
	mediaType := mime.TypeByExtension(filepath.Ext(path))
	if mediaType == "" {
		mediaType = "image/jpeg" // safe fallback
	}
	// Strip parameters (e.g. "image/jpeg; charset=...") — Anthropic wants bare type.
	if idx := strings.Index(mediaType, ";"); idx != -1 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}
	return antContentBlock{
		Type: "image",
		Source: &antImageSource{
			Type:      "base64",
			MediaType: mediaType,
			Data:      base64.StdEncoding.EncodeToString(data),
		},
	}, nil
}
