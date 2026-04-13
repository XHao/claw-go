package agent

import (
	"context"
	"log/slog"
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
