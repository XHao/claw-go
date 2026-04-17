package agent_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/XHao/claw-go/agent"
	"github.com/XHao/claw-go/provider"
)

// workerMockProvider returns fixed responses round-robin.
type workerMockProvider struct {
	responses []string
	mu        sync.Mutex
	idx       int
}

func (m *workerMockProvider) Complete(ctx context.Context, msgs []provider.Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.responses) == 0 {
		return "done", nil
	}
	r := m.responses[m.idx%len(m.responses)]
	m.idx++
	return r, nil
}

func (m *workerMockProvider) CompleteWithTools(ctx context.Context, msgs []provider.Message, tools []provider.ToolDef) (provider.CompleteResult, error) {
	r, err := m.Complete(ctx, msgs)
	return provider.CompleteResult{Content: r}, err
}

func TestRunWorkerBatch_IsolatedContext(t *testing.T) {
	tasks := []agent.WorkerTask{
		{ID: "t1", Description: "task one"},
		{ID: "t2", Description: "task two"},
	}
	p := &workerMockProvider{responses: []string{"result one", "result two"}}
	results := agent.RunWorkerBatch(context.Background(), p, tasks, nil, nil)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Error != nil {
			t.Errorf("unexpected error for task %s: %v", r.TaskID, r.Error)
		}
		if r.Output == "" {
			t.Errorf("expected non-empty output for task %s", r.TaskID)
		}
	}
}

func TestRunWorkerBatch_MaxConcurrency(t *testing.T) {
	tasks := make([]agent.WorkerTask, 5)
	for i := range tasks {
		tasks[i] = agent.WorkerTask{ID: fmt.Sprintf("t%d", i+1), Description: "task"}
	}
	p := &workerMockProvider{responses: []string{"result"}}
	results := agent.RunWorkerBatch(context.Background(), p, tasks, nil, nil)
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for _, r := range results {
		if r.Error != nil {
			t.Errorf("unexpected error for task %s: %v", r.TaskID, r.Error)
		}
	}
}
