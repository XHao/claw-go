package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/XHao/claw-go/agent"
	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
	"log/slog"
)

// evalProvider returns a fixed eval response for EvalTurn testing.
type evalProvider struct {
	evalResponse string
	reply        string
}

func (e *evalProvider) Complete(ctx context.Context, msgs []provider.Message) (string, error) {
	return e.evalResponse, nil
}

func (e *evalProvider) CompleteWithTools(ctx context.Context, msgs []provider.Message, tools []provider.ToolDef) (provider.CompleteResult, error) {
	return provider.CompleteResult{Content: e.reply, StopReason: "stop"}, nil
}

func TestAutoDistill_WritesKnowledgeWhenValuable(t *testing.T) {
	memDir := t.TempDir()
	expDir := t.TempDir()
	mgr := memory.NewManager(memDir)
	expStore := knowledge.NewExperienceStore(expDir)

	// evalProvider returns "valuable" for Complete (EvalTurn), "ok" for CompleteWithTools (LLM reply)
	ep := &evalProvider{
		evalResponse: `{"valuable":true,"topic":"docker","summary":"bridge 模式让容器互通"}`,
		reply:        "好的",
	}

	sessStore := session.NewStore(0, "", "")
	a := agent.New(ep, sessStore, slog.Default())
	a.SetMemory(mgr)
	distiller := knowledge.NewDistiller(ep, mgr, expStore)
	a.SetDistiller(distiller)

	a.Dispatch(context.Background(), channel.InboundMessage{
		SessionKey:  "s1",
		ChannelType: "test",
		ChannelName: "test",
		Text:        "docker 网络配置",
	})

	// Give the async goroutine time to complete.
	time.Sleep(100 * time.Millisecond)

	content, err := expStore.Load("docker")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "bridge") {
		t.Errorf("expected distilled content to contain 'bridge', got: %s", content)
	}
}

func TestAutoDistill_SkipsWhenNotValuable(t *testing.T) {
	memDir := t.TempDir()
	expDir := t.TempDir()
	mgr := memory.NewManager(memDir)
	expStore := knowledge.NewExperienceStore(expDir)

	ep := &evalProvider{
		evalResponse: `{"valuable":false}`,
		reply:        "好的",
	}

	sessStore := session.NewStore(0, "", "")
	a := agent.New(ep, sessStore, slog.Default())
	a.SetMemory(mgr)
	distiller := knowledge.NewDistiller(ep, mgr, expStore)
	a.SetDistiller(distiller)

	a.Dispatch(context.Background(), channel.InboundMessage{
		SessionKey:  "s1",
		ChannelType: "test",
		ChannelName: "test",
		Text:        "你好",
	})

	time.Sleep(100 * time.Millisecond)

	content, _ := expStore.Load("docker")
	if content != "" {
		t.Errorf("expected no distilled content, got: %s", content)
	}
}
