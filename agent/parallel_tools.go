package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/ipc"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/tool"
)

// readOnlyTools is the set of tools that never modify the filesystem or
// external state. All others are treated as potentially mutating.
var readOnlyTools = map[string]bool{
	"inspect_file":     true,
	"read_file":        true,
	"search_file":      true,
	"list_files":       true,
	"fetch_url":        true,
	"web_search":       true,
	"recall_memory":    true,
	"list_experiences": true,
	"show_experience":  true,
}

// shouldParallelizeToolBatch returns true when all tool calls in the batch
// can safely run concurrently. Three-layer check:
//
//  1. Single call → no parallelism needed (returns false).
//  2. All tools are read-only → safe to parallelize (returns true).
//  3. Mixed/write tools → check for path overlap; parallel only if no overlap.
func shouldParallelizeToolBatch(calls []provider.ToolCallRequest) bool {
	if len(calls) <= 1 {
		return false
	}

	// Layer 2: all read-only?
	allReadOnly := true
	for _, tc := range calls {
		if !readOnlyTools[tc.Function.Name] {
			allReadOnly = false
			break
		}
	}
	if allReadOnly {
		return true
	}

	// Layer 3: detect path conflicts between write tools and any other tool.
	//
	// Rules:
	//   - Non-read-only tool with no path (e.g. bash) → unknown side-effects → serial.
	//   - Read-only tool with no path (e.g. web_search) → safe, skip.
	//   - Write tool path is recorded in writePaths.
	//   - Any tool (read or write) whose path appears in writePaths → conflict → serial.
	//     This catches both write+write and write+read conflicts on the same file.
	//   - Two read-only tools sharing a path are always safe (concurrent reads).
	writePaths := make(map[string]bool, len(calls))
	for _, tc := range calls {
		p := extractPath(tc.Function.Arguments)
		if p == "" {
			if !readOnlyTools[tc.Function.Name] {
				return false // non-read-only, no path → unknown side-effects
			}
			continue // read-only with no path: safe
		}
		if !readOnlyTools[tc.Function.Name] {
			if writePaths[p] {
				return false // two writers on the same path
			}
			writePaths[p] = true
		}
	}
	// Second pass: check whether any tool (including reads) touches a write path.
	for _, tc := range calls {
		p := extractPath(tc.Function.Arguments)
		if p == "" {
			continue
		}
		if readOnlyTools[tc.Function.Name] && writePaths[p] {
			return false // read conflicts with a concurrent write on the same path
		}
	}
	return true
}

// extractPath parses the "path" field from a tool's JSON arguments.
// Returns "" if the field is absent or the JSON is invalid.
func extractPath(argsJSON string) string {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	return args.Path
}

// runToolBatch executes all tool calls, either concurrently or serially
// depending on shouldParallelizeToolBatch. Results are returned in the
// same order as calls.
func runToolBatch(
	ctx context.Context,
	log *slog.Logger,
	calls []provider.ToolCallRequest,
	runner *tool.LocalRunner,
	rctx tool.RunContext,
	ch channel.Channel,
	chatID string,
) []ipc.ToolResult {
	results := make([]ipc.ToolResult, len(calls))

	execOne := func(i int, tc provider.ToolCallRequest) {
		log.Info("tool call requested", "tool", tc.Function.Name, "call_id", tc.ID)
		var output string
		var isErr bool

		if runner != nil {
			toolProgress := func(m string) {
				log.Info("tool progress", "tool", tc.Function.Name, "msg", m)
				if ch != nil {
					_ = ch.Send(ctx, channel.OutboundMessage{ChatID: chatID, Delta: m + "\n"})
				}
			}
			output, isErr = runner.Run(ctx, tc.Function.Name, tc.Function.Arguments, rctx, toolProgress)
			if isErr {
				log.Warn("tool error", "tool", tc.Function.Name)
			} else {
				log.Info("tool result", "tool", tc.Function.Name)
			}
		} else {
			output = "工具不可用：守护进程未配置工具执行器"
			isErr = true
		}

		results[i] = ipc.ToolResult{
			CallID:  tc.ID,
			Name:    tc.Function.Name,
			Output:  output,
			IsError: isErr,
		}
	}

	if shouldParallelizeToolBatch(calls) {
		var wg sync.WaitGroup
		wg.Add(len(calls))
		for i, tc := range calls {
			i, tc := i, tc
			go func() {
				defer wg.Done()
				execOne(i, tc)
			}()
		}
		wg.Wait()
	} else {
		for i, tc := range calls {
			execOne(i, tc)
		}
	}

	return results
}
