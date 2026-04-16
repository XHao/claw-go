package agent

import (
	"testing"

	"github.com/XHao/claw-go/provider"
)

func makeTC(name, args string) provider.ToolCallRequest {
	return provider.ToolCallRequest{
		ID:   "id-" + name,
		Type: "function",
		Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{
			Name:      name,
			Arguments: args,
		},
	}
}

func TestShouldParallelize_SingleTool(t *testing.T) {
	calls := []provider.ToolCallRequest{makeTC("read_file", `{"path":"/a.go"}`)}
	if shouldParallelizeToolBatch(calls) {
		t.Error("single tool should not need parallelization")
	}
}

func TestShouldParallelize_AllReadOnly(t *testing.T) {
	calls := []provider.ToolCallRequest{
		makeTC("read_file", `{"path":"/a.go"}`),
		makeTC("search_file", `{"path":"/b.go","query":"foo"}`),
	}
	if !shouldParallelizeToolBatch(calls) {
		t.Error("all read-only tools should be parallelizable")
	}
}

func TestShouldParallelize_WriteToolNoPathOverlap(t *testing.T) {
	calls := []provider.ToolCallRequest{
		makeTC("write_file", `{"path":"/a.go","content":"x"}`),
		makeTC("write_file", `{"path":"/b.go","content":"y"}`),
	}
	if !shouldParallelizeToolBatch(calls) {
		t.Error("write tools with distinct paths should be parallelizable")
	}
}

func TestShouldParallelize_WriteToolPathOverlap(t *testing.T) {
	calls := []provider.ToolCallRequest{
		makeTC("write_file", `{"path":"/a.go","content":"x"}`),
		makeTC("read_file", `{"path":"/a.go"}`),
	}
	if shouldParallelizeToolBatch(calls) {
		t.Error("overlapping paths should force serial execution")
	}
}

func TestShouldParallelize_BashAlwaysSerial(t *testing.T) {
	calls := []provider.ToolCallRequest{
		makeTC("bash", `{"command":"ls"}`),
		makeTC("read_file", `{"path":"/a.go"}`),
	}
	if shouldParallelizeToolBatch(calls) {
		t.Error("bash is not in read-only whitelist, mixed batch should be serial")
	}
}

func TestShouldParallelize_WriteAndTwoReadsOnSamePath(t *testing.T) {
	// write /b.go + two reads of /a.go — reads share a path but no write touches /a.go.
	// Should be parallelizable: write is on a different path, concurrent reads are safe.
	calls := []provider.ToolCallRequest{
		makeTC("write_file", `{"path":"/b.go","content":"x"}`),
		makeTC("read_file", `{"path":"/a.go"}`),
		makeTC("inspect_file", `{"path":"/a.go"}`),
	}
	if !shouldParallelizeToolBatch(calls) {
		t.Error("two reads of /a.go alongside write to /b.go should be parallelizable")
	}
}

func TestShouldParallelize_WriteWithReadOnlyNoPath(t *testing.T) {
	// write_file has a path; web_search has no path but is read-only.
	// Layer 3 should skip web_search (read-only) and only check write_file's path.
	// No overlap → should be parallelizable.
	calls := []provider.ToolCallRequest{
		makeTC("write_file", `{"path":"/a.go","content":"x"}`),
		makeTC("web_search", `{"query":"golang concurrency"}`),
	}
	if !shouldParallelizeToolBatch(calls) {
		t.Error("write_file + web_search (no path overlap) should be parallelizable")
	}
}
