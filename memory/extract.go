package memory

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/XHao/claw-go/ipc"
	"github.com/XHao/claw-go/provider"
)

const (
	maxUserChars  = 240 // runes kept from user message
	maxReplyChars = 240 // runes kept from assistant reply
	maxCmdChars   = 80  // runes kept from bash command string
)

// trunc shortens s to at most n runes, appending "…" if cut.
// Leading / trailing whitespace is always removed first.
func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// ExtractActions builds an []Action from parallel slices of LLM tool-call
// requests and their execution results.  Both slices must have the same length.
func ExtractActions(calls []provider.ToolCallRequest, results []ipc.ToolResult) []Action {
	out := make([]Action, 0, len(calls))
	for i, tc := range calls {
		var isErr bool
		if i < len(results) {
			isErr = results[i].IsError
		}
		out = append(out, parseAction(tc.Function.Name, tc.Function.Arguments, isErr))
	}
	return out
}

// parseAction converts a single tool call into a compact Action using only
// the call arguments (no output text is stored).
func parseAction(name, argsJSON string, isErr bool) Action {
	switch name {
	case "bash":
		var args struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		status := "ok"
		if isErr {
			status = "err"
		}
		return Action{
			Tool:    "bash",
			Summary: fmt.Sprintf("bash: `%s`  [%s]", trunc(args.Command, maxCmdChars), status),
			IsError: isErr,
		}

	case "read_file":
		var args struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		return Action{Tool: "read_file", Summary: "read " + args.Path, Path: args.Path}

	case "write_file":
		var args struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		size := len(args.Content)
		var sizeStr string
		switch {
		case size < 1024:
			sizeStr = fmt.Sprintf("%d B", size)
		case size < 1024*1024:
			sizeStr = fmt.Sprintf("%.1f KB", float64(size)/1024)
		default:
			sizeStr = fmt.Sprintf("%.1f MB", float64(size)/1024/1024)
		}
		return Action{
			Tool:    "write_file",
			Summary: fmt.Sprintf("wrote %s → %s", sizeStr, args.Path),
			Path:    args.Path,
			IsError: isErr,
		}

	case "list_files":
		var args struct {
			Dir string `json:"dir"`
		}
		_ = json.Unmarshal([]byte(argsJSON), &args)
		return Action{Tool: "list_files", Summary: "ls " + args.Dir, Path: args.Dir}

	default:
		return Action{Tool: name, Summary: fmt.Sprintf("called %s", name), IsError: isErr}
	}
}

// CollectArtifacts returns a deduplicated, ordered list of file paths from
// all read_file and write_file actions.
func CollectArtifacts(actions []Action) []string {
	seen := make(map[string]bool)
	var out []string
	for _, a := range actions {
		if a.Path == "" {
			continue
		}
		if a.Tool != "read_file" && a.Tool != "write_file" {
			continue
		}
		if !seen[a.Path] {
			seen[a.Path] = true
			out = append(out, a.Path)
		}
	}
	return out
}

// BuildSummary assembles a complete TurnSummary from raw turn data.
// n is the turn index (call sess.TurnCount() right after appending the user
// message to get a stable, 1-based value).
func BuildSummary(n int, userText, replyText string, actions []Action, iters int, isError bool) TurnSummary {
	return TurnSummary{
		N:       n,
		At:      time.Now().UTC(),
		User:    trunc(userText, maxUserChars),
		Reply:   trunc(replyText, maxReplyChars),
		Actions: actions,
		Files:   CollectArtifacts(actions),
		Iters:   iters,
		IsError: isError,
	}
}
