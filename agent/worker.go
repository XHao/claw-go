// Package agent — worker.go implements concurrent Worker Agent execution.
package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
	"github.com/XHao/claw-go/tool"
)

const (
	maxWorkerConcurrency = 3
	maxWorkerIterations  = 20
)

// WorkerTask describes a single sub-task to be executed by a Worker Agent.
type WorkerTask struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	ToolsHint   []string `json:"tools_hint,omitempty"`
}

// WorkerResult holds the output of a single Worker execution.
type WorkerResult struct {
	TaskID string
	Output string
	Error  error
}

// RunWorkerBatch executes tasks concurrently (max 3) using isolated Agent instances.
// Each worker has its own empty session, inherits the provided tool runner,
// and cannot call plan_tasks (preventing recursive planning).
// p is the shared LLM provider; toolRunner may be nil (disables tools).
// systemPrompt is injected into each worker's session when non-nil.
func RunWorkerBatch(
	ctx context.Context,
	p provider.Provider,
	tasks []WorkerTask,
	toolRunner *tool.LocalRunner,
	systemPrompt *string,
) []WorkerResult {
	results := make([]WorkerResult, len(tasks))
	sem := make(chan struct{}, maxWorkerConcurrency)

	var wg sync.WaitGroup
	for i, task := range tasks {
		i, task := i, task
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runSingleWorker(ctx, p, task, toolRunner, systemPrompt)
		}()
	}
	wg.Wait()
	return results
}

// runSingleWorker runs one Worker Agent for a single task in an isolated session.
func runSingleWorker(
	ctx context.Context,
	p provider.Provider,
	task WorkerTask,
	toolRunner *tool.LocalRunner,
	systemPrompt *string,
) WorkerResult {
	sp := ""
	if systemPrompt != nil {
		sp = *systemPrompt
	}
	// Empty dir = no disk persistence for worker sessions.
	workerSessions := session.NewStore(maxWorkerIterations, sp, "")
	workerSessions.Get("worker")

	// Inherit parent tools but remove plan_tasks to prevent recursive planning.
	var workerRunner *tool.LocalRunner
	if toolRunner != nil {
		workerRunner = toolRunner.WithoutTools("plan_tasks")
	}

	workerLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	ag := New(p, workerSessions, workerLog)
	ag.SetMaxIterations(maxWorkerIterations)
	if workerRunner != nil {
		ag.SetToolRunner(workerRunner)
	}

	reply, err := ag.InjectMessage(ctx, "worker", task.Description)
	if err != nil {
		return WorkerResult{TaskID: task.ID, Error: fmt.Errorf("worker %s: %w", task.ID, err)}
	}
	return WorkerResult{TaskID: task.ID, Output: reply}
}
