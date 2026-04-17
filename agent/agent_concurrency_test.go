package agent_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/XHao/claw-go/agent"
	"github.com/XHao/claw-go/agentdef"
	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
)

// concurrentMockProvider is safe for concurrent use.
type concurrentMockProvider struct{}

func (m *concurrentMockProvider) Complete(ctx context.Context, msgs []provider.Message) (string, error) {
	return "ok", nil
}

func (m *concurrentMockProvider) CompleteWithTools(ctx context.Context, msgs []provider.Message, tools []provider.ToolDef) (provider.CompleteResult, error) {
	return provider.CompleteResult{Content: "ok"}, nil
}

// TestDispatch_ConcurrentMultiAgent verifies no data race when multiple sessions
// with different agentIDs dispatch concurrently.
func TestDispatch_ConcurrentMultiAgent(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"default", "lawyer"} {
		dir := filepath.Join(tmp, name)
		os.MkdirAll(dir, 0o700)
		os.WriteFile(filepath.Join(dir, "persona.md"), []byte("persona: "+name), 0o600)
	}
	reg, err := agentdef.LoadRegistry(tmp)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	lg := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	sessions := session.NewStore(10, "system", "")
	p := &concurrentMockProvider{}
	ag := agent.New(p, sessions, lg)
	ag.SetAgentRegistry(reg)

	var wg sync.WaitGroup
	agentIDs := []string{"default", "lawyer"}
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessionKey := fmt.Sprintf("session-%d", i)
			agentID := agentIDs[i%2]
			sessions.SetAgentID(sessionKey, agentID)
			ag.Dispatch(context.Background(), channel.InboundMessage{
				SessionKey: sessionKey,
				ChatID:     sessionKey,
				Text:       "hello",
			})
		}()
	}
	wg.Wait()
}
