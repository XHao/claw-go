// Package provider defines the LLM provider abstraction.
package provider

import (
	"context"
	"encoding/json"
)

// Message is a single chat message.
// Supports plain text messages, assistant tool-call requests, and tool results.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`

	// Set on assistant messages that contain tool call requests.
	ToolCalls []ToolCallRequest `json:"tool_calls,omitempty"`

	// Set on "tool" role messages that carry a function result.
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"` // tool function name
}

// ToolDef describes a tool that the LLM may call.
type ToolDef struct {
	Name        string          // unique identifier
	Description string          // shown to the model
	Parameters  json.RawMessage // JSON Schema object for the arguments
}

// ToolCallRequest is an individual tool invocation emitted by the LLM
// (OpenAI "function" format).
type ToolCallRequest struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // always "function"
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON string
	} `json:"function"`
}

// ToolResult carries the output of a locally-executed tool back to the LLM.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error,omitempty"`
}

// Usage holds token consumption reported by the LLM API for one call.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// ModelMeta describes which concrete model actually handled the call.
//
// ModelKey is the logical model name from config.models (when available),
// while Model is the provider-native model identifier (e.g. gpt-4o-mini).
type ModelMeta struct {
	ModelKey string
	Model    string
}

// CompleteResult is returned by CompleteWithTools.
// Exactly one of Content or ToolCalls will be non-zero.
type CompleteResult struct {
	Content    string            // final text reply
	ToolCalls  []ToolCallRequest // tool calls requested by the model
	StopReason string
	Usage      Usage // token counts reported by the API
	Model      ModelMeta
}

// Provider is the interface that any LLM backend must implement.
type Provider interface {
	// Complete is the simple text-only completion (no tools).
	Complete(ctx context.Context, messages []Message) (string, error)

	// CompleteWithTools sends messages along with tool definitions and returns
	// either a final content reply or a set of tool call requests.
	CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error)
}
