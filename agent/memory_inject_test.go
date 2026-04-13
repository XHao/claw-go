package agent_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/XHao/claw-go/agent"
	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
)

// memStubProvider records messages received and returns a fixed reply.
type memStubProvider struct {
	received [][]provider.Message
	reply    string
}

func (s *memStubProvider) CompleteWithTools(ctx context.Context, msgs []provider.Message, tools []provider.ToolDef) (provider.CompleteResult, error) {
	s.received = append(s.received, msgs)
	return provider.CompleteResult{Content: s.reply, StopReason: "stop"}, nil
}

func (s *memStubProvider) Complete(ctx context.Context, msgs []provider.Message) (string, error) {
	return s.reply, nil
}

func TestDispatch_L2AutoInject_InjectsRelevantHistory(t *testing.T) {
	memDir := t.TempDir()
	mgr := memory.NewManager(memDir)

	// Seed a relevant past turn in a different session.
	sess0 := mgr.ForSession("old-session")
	_ = sess0.SaveTurn(memory.TurnSummary{
		N: 1, At: time.Now().Add(-24 * time.Hour),
		User:  "docker compose 网络配置问题",
		Reply: "使用 bridge 模式",
	})

	stub := &memStubProvider{reply: "好的"}
	sessStore := session.NewStore(0, "", "")
	a := agent.New(stub, sessStore, slog.Default())
	a.SetMemory(mgr)

	a.Dispatch(context.Background(), channel.InboundMessage{
		SessionKey:  "new-session",
		ChannelType: "test",
		ChannelName: "test",
		Text:        "docker 网络怎么配置",
	})

	if len(stub.received) == 0 {
		t.Fatal("no LLM calls recorded")
	}
	msgs := stub.received[0]
	found := false
	for _, m := range msgs {
		if m.Role == "system" && strings.Contains(m.Content, "docker") {
			found = true
			break
		}
	}
	if !found {
		t.Error("L2 history injection not found in LLM messages")
	}
}

func TestDispatch_L2AutoInject_SkipsWhenNoMemory(t *testing.T) {
	stub := &memStubProvider{reply: "好的"}
	sessStore := session.NewStore(0, "", "")
	a := agent.New(stub, sessStore, slog.Default())
	// No memory set — should not panic

	a.Dispatch(context.Background(), channel.InboundMessage{
		SessionKey:  "s1",
		ChannelType: "test",
		ChannelName: "test",
		Text:        "docker 网络",
	})

	if len(stub.received) == 0 {
		t.Fatal("no LLM calls recorded")
	}
}
