package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/XHao/claw-go/agent"
	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
	"log/slog"
)

// classifyAndCaptureProvider: Complete returns classify JSON, CompleteWithTools returns "ok".
type classifyAndCaptureProvider struct {
	classifyResp string
	received     [][]provider.Message
}

func (c *classifyAndCaptureProvider) Complete(ctx context.Context, msgs []provider.Message) (string, error) {
	return c.classifyResp, nil
}

func (c *classifyAndCaptureProvider) CompleteWithTools(ctx context.Context, msgs []provider.Message, tools []provider.ToolDef) (provider.CompleteResult, error) {
	c.received = append(c.received, msgs)
	return provider.CompleteResult{Content: "ok", StopReason: "stop"}, nil
}

func TestDispatch_InjectsProcedureWhenTagMatches(t *testing.T) {
	procDir := t.TempDir()
	procStore := knowledge.NewProcedureStore(procDir)
	_ = procStore.Save("debug-golang", knowledge.ProcedureFile{
		Name:     "Golang 调试流程",
		Tags:     []string{"debug", "golang"},
		Priority: 10,
		Body:     "遇到 panic 先跑 go test -race",
	})

	cp := &classifyAndCaptureProvider{classifyResp: `{"tags":["debug","golang"]}`}
	classifier := knowledge.NewTaskClassifier(cp)
	sessStore := session.NewStore(0, "", "")
	a := agent.New(cp, sessStore, slog.Default())
	a.SetProcedureStore(procStore)
	a.SetTaskClassifier(classifier)

	a.Dispatch(context.Background(), channel.InboundMessage{
		SessionKey:  "s1",
		ChannelType: "test",
		ChannelName: "test",
		Text:        "goroutine 泄漏怎么排查",
	})

	if len(cp.received) == 0 {
		t.Fatal("no LLM calls recorded")
	}
	found := false
	for _, msgs := range cp.received {
		for _, m := range msgs {
			if m.Role == "system" && strings.Contains(m.Content, "go test -race") {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("procedure content not injected into LLM messages")
	}
}

func TestDispatch_SkipsProcedureWhenNoStoreSet(t *testing.T) {
	cp := &classifyAndCaptureProvider{classifyResp: `{"tags":["debug"]}`}
	sessStore := session.NewStore(0, "", "")
	a := agent.New(cp, sessStore, slog.Default())
	// No SetProcedureStore / SetTaskClassifier — should not panic

	a.Dispatch(context.Background(), channel.InboundMessage{
		SessionKey:  "s1",
		ChannelType: "test",
		ChannelName: "test",
		Text:        "hello",
	})

	if len(cp.received) == 0 {
		t.Fatal("no LLM calls recorded")
	}
}
