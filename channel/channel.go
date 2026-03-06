// Package channel defines the messaging channel abstraction.
package channel

import (
	"context"
	"time"

	"github.com/XHao/claw-go/ipc"
)

// InboundMessage is a normalised message received from any channel.
type InboundMessage struct {
	ChannelName string
	ChannelType string
	SessionKey  string
	ChatID      string
	UserID      string
	Username    string
	Text        string
	MessageID   string
	Timestamp   time.Time
}

// OutboundMessage is a message to send through a channel.
type OutboundMessage struct {
	ChatID           string
	Text             string
	ReplyToMessageID string
}

// DispatchFunc is called by the channel for each inbound user message.
// exchange may be nil when the channel or session doesn't support tool calls.
type DispatchFunc func(msg InboundMessage, exchange ipc.ToolExchangeFn)

// Channel is the interface every messaging integration must implement.
type Channel interface {
	ID() string
	Start(ctx context.Context, dispatch DispatchFunc) error
	Send(ctx context.Context, msg OutboundMessage) error
	Status() Status
}

// Status represents the runtime health of a channel.
type Status struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}
