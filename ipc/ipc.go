// Package ipc defines the Unix Domain Socket protocol used between the
// claw daemon and CLI clients.
package ipc

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/XHao/claw-go/dirs"
)

const (
	// DefaultScannerBuffer is the initial token buffer for newline-delimited IPC frames.
	DefaultScannerBuffer = 64 * 1024
	// MaxFrameBytes is the maximum JSON frame size accepted over the socket.
	// It must be comfortably larger than tool results such as read_file output.
	MaxFrameBytes = 1024 * 1024
)

// DefaultSocketPath returns the default Unix socket path.
func DefaultSocketPath() string { return dirs.SocketPath() }

// NewScanner returns a newline-delimited JSON scanner configured for larger IPC frames.
func NewScanner(r io.Reader) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, DefaultScannerBuffer), MaxFrameBytes)
	return s
}

// ToolCall is a tool invocation request sent from daemon → client.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"` // parsed JSON arguments
}

// ToolResult is a tool execution result sent from client → daemon.
type ToolResult struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error,omitempty"`
}

// LLMUsageEvent is emitted by the daemon after each LLM call.
// It enables realtime token and throughput telemetry in the CLI.
type LLMUsageEvent struct {
	At               string `json:"at"`
	Hint             string `json:"hint,omitempty"`
	Source           string `json:"source,omitempty"`
	ModelKey         string `json:"model_key,omitempty"`
	Model            string `json:"model,omitempty"`
	InputTokensEst   int    `json:"input_tokens_est,omitempty"`
	ContextTokensEst int    `json:"context_tokens_est,omitempty"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	LatencyMs        int64  `json:"latency_ms"`
	StopReason       string `json:"stop_reason,omitempty"`
	IsError          bool   `json:"is_error,omitempty"`
}

// Msg is the newline-delimited JSON frame exchanged over the socket.
//
// Server → client (connection phase):
//
//	{"sessions":[...]}                    list of conversations
//
// Client → server (selection phase):
//
//	{"cmd":"select","session":"name"}
//	{"cmd":"new","session":"name"}
//
// Regular chat phase:
//
//	Client → server: {"text":"...","cwd":"/path/to/dir"}
//	                 {"cmd":"reset"} | {"cmd":"ping"}
//	Server → client: {"reply":"..."}
//	                 {"delta":"..."}    (streaming: incremental text chunk)
//	                 {"info":"..."} | {"error":"..."}
//	                 {"cmd":"inject_ctx","text":"..."}  (experience context injection) - client→server

type Msg struct {
	// chat
	Text  string `json:"text,omitempty"`
	Cmd   string `json:"cmd,omitempty"`
	Reply string `json:"reply,omitempty"`
	Delta string `json:"delta,omitempty"` // streaming: incremental text chunk
	Info  string `json:"info,omitempty"`
	Error string `json:"error,omitempty"`
	// client environment context
	Cwd string `json:"cwd,omitempty"` // client working directory, sent with each text message
	// session management
	Session  string         `json:"session,omitempty"`
	Sessions []SessionInfo  `json:"sessions,omitempty"`
	Usage    *LLMUsageEvent `json:"usage,omitempty"`
	// recent history sent with select-ack
	History []HistoryEntry `json:"history,omitempty"`
	// agent management
	AgentID   string `json:"agent_id,omitempty"`   // current bound agent id
	AgentName string `json:"agent_name,omitempty"` // human-readable agent name for UI display
}

// SessionInfo describes a stored conversation visible to the client.
type SessionInfo struct {
	Name      string `json:"name"`
	TurnCount int    `json:"turn_count"`
	Active    bool   `json:"active,omitempty"`
	AgentID   string `json:"agent_id,omitempty"` // bound agent for this session
}

// HistoryEntry is a single message in the conversation history,
// sent to the client after selecting a session.
type HistoryEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
