package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAIProvider implements Provider against the OpenAI Chat Completions API
// (or any compatible endpoint such as Ollama, Azure, local proxies).
type OpenAIProvider struct {
	baseURL    string
	apiKey     string
	model      string
	maxTokens  int
	httpClient *http.Client
}

// NewOpenAI creates an OpenAIProvider.
func NewOpenAI(baseURL, apiKey, model string, maxTokens, timeoutSeconds int) *OpenAIProvider {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 120
	}
	return &OpenAIProvider{
		baseURL:   baseURL,
		apiKey:    apiKey,
		model:     model,
		maxTokens: maxTokens,
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
	Model      string      `json:"model"`
	Messages   []oaMessage `json:"messages"`
	MaxTokens  int         `json:"max_tokens,omitempty"`
	Stream     bool        `json:"stream"`
	Tools      []oaTool    `json:"tools,omitempty"`
	ToolChoice string      `json:"tool_choice,omitempty"`
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
func (p *OpenAIProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	// Build wire messages.
	oaMsgs := make([]oaMessage, len(messages))
	for i, m := range messages {
		content := m.Content
		// Gemini (via OpenAI-compat) rejects tool messages whose content field is
		// missing. Since oaMessage.Content uses omitempty, an empty string would be
		// omitted. Use a placeholder so the field is always present.
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

	req := oaRequest{
		Model:     p.model,
		Messages:  oaMsgs,
		MaxTokens: p.maxTokens,
		Stream:    false,
		Tools:     oaTools,
	}
	if len(oaTools) > 0 {
		req.ToolChoice = "auto"
	}

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

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("openai: read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return CompleteResult{}, fmt.Errorf("openai: status %d: %s", resp.StatusCode, string(rawBody))
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
		}, nil
	}
	content := choice.Message.Content
	// Some models (e.g. Gemini via OpenAI-compat) return null/empty content on
	// the final turn after a tool-call chain. Substitute a safe fallback so the
	// client loop is never blocked by an empty reply frame.
	if content == "" {
		content = "(done)"
	}
	return CompleteResult{
		Content:    content,
		StopReason: choice.FinishReason,
		Usage:      usage,
	}, nil
}
