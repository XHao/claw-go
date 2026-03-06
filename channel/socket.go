// Package channel - Unix Domain Socket channel (server/daemon side).
package channel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/XHao/claw-go/ipc"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
)

// connState holds a single client's connection and encoder.
type connState struct {
	conn    net.Conn
	encoder *json.Encoder
}

// SocketChannel listens on a Unix Domain Socket. Multiple clients may connect
// simultaneously, but each named conversation accepts only one client at a time.
type SocketChannel struct {
	name       string
	socketPath string
	sessions   *session.Store
	running    atomic.Bool

	mu          sync.Mutex
	activeConns map[string]*connState // key = conversation name
}

// NewSocketChannel creates a SocketChannel.
// socketPath="" uses ipc.DefaultSocketPath().
func NewSocketChannel(name, socketPath string, sessions *session.Store) *SocketChannel {
	if socketPath == "" {
		socketPath = ipc.DefaultSocketPath()
	}
	return &SocketChannel{
		name:        name,
		socketPath:  socketPath,
		sessions:    sessions,
		activeConns: make(map[string]*connState),
	}
}

// ID returns the unique channel identifier.
func (s *SocketChannel) ID() string { return "socket:" + s.name }

// Start listens and serves clients until ctx is cancelled.
func (s *SocketChannel) Start(ctx context.Context, dispatch DispatchFunc) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o700); err != nil {
		return fmt.Errorf("socket: mkdir: %w", err)
	}
	_ = os.Remove(s.socketPath)

	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("socket: listen %q: %w", s.socketPath, err)
	}
	defer func() {
		ln.Close()
		os.Remove(s.socketPath)
		s.running.Store(false)
	}()
	s.running.Store(true)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("socket: accept: %w", err)
		}
		go s.handleConn(ctx, conn, dispatch)
	}
}

func (s *SocketChannel) handleConn(ctx context.Context, conn net.Conn, dispatch DispatchFunc) {
	defer conn.Close()
	enc := json.NewEncoder(conn)
	// One scanner for the entire connection lifetime.
	// Critical: do NOT create a second bufio.Scanner on the same conn; the
	// first scanner buffers ahead and any bytes it pre-read would be lost.
	scanner := bufio.NewScanner(conn)

	// Phase 1: conversation selection (no lock held yet).
	sessionName, err := s.runSelectionPhase(enc, scanner)
	if err != nil {
		// error already sent inside runSelectionPhase
		return
	}
	// Remove the connection slot when the chat loop exits (connection closed).
	defer func() {
		s.mu.Lock()
		delete(s.activeConns, sessionName)
		s.mu.Unlock()
	}()

	// Phase 2: chat loop (reuses the same scanner).
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		var msg ipc.Msg
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			_ = enc.Encode(ipc.Msg{Error: "invalid message"})
			continue
		}
		switch msg.Cmd {
		case "ping":
			_ = enc.Encode(ipc.Msg{Info: "pong"})
		case "inject_ctx":
			// Client is mounting an experience document as extra session context.
			if msg.Text == "" {
				_ = enc.Encode(ipc.Msg{Error: "inject_ctx: empty content"})
			} else {
				s.sessions.InjectContext(sessionName, msg.Text)
				_ = enc.Encode(ipc.Msg{Info: "context injected"})
			}
		case "reset":
			dispatch(InboundMessage{
				ChannelName: s.name,
				ChannelType: "socket",
				SessionKey:  sessionName,
				ChatID:      sessionName,
				Text:        "/reset",
				Timestamp:   time.Now(),
			}, nil)
		default:
			if msg.Text == "" {
				continue
			}
			// Build a synchronous tool-exchange function that encodes tool calls
			// to this connection and blocks until the client sends tool_results.
			exchange := func(calls []ipc.ToolCall) ([]ipc.ToolResult, error) {
				if err := enc.Encode(ipc.Msg{ToolCalls: calls}); err != nil {
					return nil, fmt.Errorf("tool_calls send: %w", err)
				}
				// Block until client sends tool_results.
				if !scanner.Scan() {
					return nil, fmt.Errorf("connection closed waiting for tool_results")
				}
				var res ipc.Msg
				if err := json.Unmarshal(scanner.Bytes(), &res); err != nil {
					return nil, fmt.Errorf("tool_results decode: %w", err)
				}
				if res.Cmd != "tool_results" {
					return nil, fmt.Errorf("expected tool_results, got cmd=%q", res.Cmd)
				}
				return res.ToolResults, nil
			}
			dispatch(InboundMessage{
				ChannelName: s.name,
				ChannelType: "socket",
				SessionKey:  sessionName,
				ChatID:      sessionName,
				UserID:      "local",
				Text:        msg.Text,
				Timestamp:   time.Now(),
			}, exchange)
		}
	}
}

// runSelectionPhase sends the session list, waits for a valid select/new
// command, registers the connection, and returns the chosen session name.
func (s *SocketChannel) runSelectionPhase(enc *json.Encoder, scanner *bufio.Scanner) (string, error) {
	for {
		// Build fresh list each attempt so Active flags are up-to-date.
		sums := s.sessions.List()
		infos := make([]ipc.SessionInfo, len(sums))
		s.mu.Lock()
		for i, sum := range sums {
			infos[i] = ipc.SessionInfo{
				Name:      sum.Name,
				TurnCount: sum.TurnCount,
				Active:    s.activeConns[sum.Name] != nil,
			}
		}
		s.mu.Unlock()

		if err := enc.Encode(ipc.Msg{Sessions: infos}); err != nil {
			return "", fmt.Errorf("send session list: %w", err)
		}

		if !scanner.Scan() {
			return "", fmt.Errorf("connection closed during selection")
		}
		var msg ipc.Msg
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			_ = enc.Encode(ipc.Msg{Error: "invalid message"})
			// loop – resend list
			continue
		}

		name := strings.TrimSpace(msg.Session)
		if name == "" {
			_ = enc.Encode(ipc.Msg{Error: "session name must not be empty"})
			continue
		}

		switch msg.Cmd {
		case "select":
			if !s.sessions.Has(name) {
				_ = enc.Encode(ipc.Msg{Error: fmt.Sprintf("conversation %q not found", name)})
				continue
			}
			// Try to claim the slot for this conversation.
			s.mu.Lock()
			if s.activeConns[name] != nil {
				s.mu.Unlock()
				_ = enc.Encode(ipc.Msg{Error: fmt.Sprintf("conversation %q is already in use by another client", name)})
				continue
			}
			s.activeConns[name] = &connState{encoder: enc}
			s.mu.Unlock()
			// Include recent history (last 3 user+assistant pairs) in the ack.
			recent := recentHistory(s.sessions.Get(name).History(), 3)
			_ = enc.Encode(ipc.Msg{Info: "resumed: " + name, History: recent})
			return name, nil

		case "new":
			// Create the session first (validates name uniqueness).
			if _, err := s.sessions.Create(name); err != nil {
				_ = enc.Encode(ipc.Msg{Error: err.Error()})
				continue
			}
			s.mu.Lock()
			s.activeConns[name] = &connState{encoder: enc}
			s.mu.Unlock()
			_ = enc.Encode(ipc.Msg{Info: "created: " + name})
			return name, nil

		default:
			_ = enc.Encode(ipc.Msg{Error: `expected cmd "select" or "new"`})
		}
	}
}

// recentHistory converts the last n user+assistant pairs from a message
// history slice into lightweight ipc.HistoryEntry records.
// Tool messages (role="tool") and bare tool-call assistant turns are skipped.
func recentHistory(msgs []provider.Message, n int) []ipc.HistoryEntry {
	// Collect only user and assistant text messages (skip tool roles).
	var filtered []provider.Message
	for _, m := range msgs {
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "" {
			filtered = append(filtered, m)
		}
	}
	// Take at most n*2 messages from the tail (n pairs).
	max := n * 2
	if len(filtered) > max {
		filtered = filtered[len(filtered)-max:]
	}
	out := make([]ipc.HistoryEntry, len(filtered))
	for i, m := range filtered {
		out[i] = ipc.HistoryEntry{Role: m.Role, Content: m.Content}
	}
	return out
}

// Send writes the assistant reply to the client that owns the given conversation.
func (s *SocketChannel) Send(_ context.Context, msg OutboundMessage) error {
	s.mu.Lock()
	state := s.activeConns[msg.ChatID]
	s.mu.Unlock()
	if state == nil {
		return nil
	}
	return state.encoder.Encode(ipc.Msg{Reply: msg.Text})
}

// Status returns the channel health.
func (s *SocketChannel) Status() Status {
	s.mu.Lock()
	count := len(s.activeConns)
	s.mu.Unlock()
	note := ""
	if count > 0 {
		note = fmt.Sprintf("%d conversation(s) with active client", count)
	}
	return Status{ID: s.ID(), Type: "socket", Name: s.name, Running: s.running.Load(), Error: note}
}
