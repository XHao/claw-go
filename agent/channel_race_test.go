package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
)

// stubChannel is a minimal channel.Channel for testing.
type stubChannel struct{ id string }

func (s *stubChannel) ID() string                                              { return s.id }
func (s *stubChannel) Start(_ context.Context, _ channel.DispatchFunc) error  { return nil }
func (s *stubChannel) Send(_ context.Context, _ channel.OutboundMessage) error { return nil }
func (s *stubChannel) Status() channel.Status                                  { return channel.Status{} }

// stubProvider is a minimal provider.Provider that immediately returns.
type stubProvider struct{}

func (s *stubProvider) Complete(_ context.Context, _ []provider.Message) (string, error) {
	return "ok", nil
}
func (s *stubProvider) CompleteWithTools(_ context.Context, _ []provider.Message, _ []provider.ToolDef) (provider.CompleteResult, error) {
	return provider.CompleteResult{Content: "ok"}, nil
}

// TestClearAutoInjected_DeletesAllMatchingKeys verifies that clearAutoInjected
// removes all keys with the given session prefix, even when there are many
// concurrent entries in the sync.Map. This guards against the Range-and-delete
// pattern that may miss entries under concurrent load.
func TestClearAutoInjected_DeletesAllMatchingKeys(t *testing.T) {
	st := session.NewStore(10, "sys", "")
	a := New(&stubProvider{}, st, slog.Default())

	const sessionKey = "my-session"
	const otherKey = "other-session"

	// Store 20 entries for our session and 5 for another.
	for i := 0; i < 20; i++ {
		a.autoInjected.Store(sessionKey+":topic-"+string(rune('a'+i)), true)
	}
	for i := 0; i < 5; i++ {
		a.autoInjected.Store(otherKey+":topic-"+string(rune('a'+i)), true)
	}

	a.clearAutoInjected(sessionKey)

	// All keys for sessionKey must be gone.
	remaining := 0
	a.autoInjected.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), sessionKey+":") {
			remaining++
		}
		return true
	})
	if remaining != 0 {
		t.Errorf("clearAutoInjected left %d keys for session %q, want 0", remaining, sessionKey)
	}

	// Keys for other session must be untouched.
	otherRemaining := 0
	a.autoInjected.Range(func(k, _ any) bool {
		if strings.HasPrefix(k.(string), otherKey+":") {
			otherRemaining++
		}
		return true
	})
	if otherRemaining != 5 {
		t.Errorf("clearAutoInjected removed keys for other session, want 5 got %d", otherRemaining)
	}
}

// TestRegisterChannel_ConcurrentWithDispatch verifies that RegisterChannel and
// Dispatch can be called concurrently without a data race on the channels map.
// Run with: go test -race ./agent/ -run TestRegisterChannel_ConcurrentWithDispatch
func TestRegisterChannel_ConcurrentWithDispatch(t *testing.T) {
	st := session.NewStore(10, "sys", "")
	a := New(&stubProvider{}, st, slog.Default())

	// Pre-register one channel so Dispatch has something to look up.
	a.RegisterChannel(&stubChannel{id: "type:name"})

	var wg sync.WaitGroup
	// Goroutine 1: keep registering new channels.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			a.RegisterChannel(&stubChannel{id: "type:name"})
		}
	}()

	// Goroutine 2: keep dispatching (reads channels map).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			a.Dispatch(context.Background(), channel.InboundMessage{
				ChannelType: "type",
				ChannelName: "name",
				SessionKey:  "sess",
				ChatID:      "chat",
				Text:        "hello",
			})
		}
	}()

	wg.Wait()
}
