// Package channel defines the messaging channel abstraction.
package channel

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/XHao/claw-go/ipc"
)

// Attachment represents a downloaded media file attached to an inbound message.
// Path is a local temporary file; the caller (agent.Dispatch) is responsible
// for deleting it after the message has been fully processed.
type Attachment struct {
	Path     string // absolute path to local temp file
	MimeType string // e.g. "image/jpeg", "image/png"
	AltText  string // fallback text if image cannot be sent, e.g. "[图片]"
}

// InboundMessage is a normalised message received from any channel.
type InboundMessage struct {
	ChannelName string
	ChannelType string
	SessionKey  string
	ChatID      string
	UserID      string
	Username    string
	Text        string
	Cwd         string // working directory reported by the client
	MessageID   string
	Timestamp   time.Time
	Attachments []Attachment // downloaded media files; caller must clean up after dispatch
}

// OutboundMessage is a message to send through a channel.
type OutboundMessage struct {
	ChatID           string
	Text             string
	Delta            string // streaming: incremental text chunk
	ReplyToMessageID string
	Usage            *ipc.LLMUsageEvent
}

// DispatchFunc is called by the channel for each inbound user message.
// ctx is derived from the channel's run context and may be cancelled by a
// client "cancel" command, allowing the agent to abort in-flight LLM calls.
type DispatchFunc func(ctx context.Context, msg InboundMessage)

// Channel is the interface every messaging integration must implement.
type Channel interface {
	ID() string
	Start(ctx context.Context, dispatch DispatchFunc) error
	Send(ctx context.Context, msg OutboundMessage) error
	Status() Status
}

// maxSessions is the maximum number of entries kept in per-channel session/webhook
// maps. When the limit is reached, the oldest half is evicted to bound memory use.
const maxSessions = 10_000

// mustMarshal encodes v to JSON and panics if encoding fails.
// Use only for values whose types guarantee successful marshalling (structs,
// maps with string keys, slices). Never pass channels, functions, or complex
// interface values.
func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("channel: mustMarshal: " + err.Error())
	}
	return b
}

// Status represents the runtime health of a channel.
type Status struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

// mimeFromPath returns the MIME type for a file based on its extension.
// Falls back to "image/jpeg" for unknown extensions.
func mimeFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/jpeg"
	}
}
