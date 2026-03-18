package agent

import (
	"encoding/json"
	"testing"

	"github.com/XHao/claw-go/ipc"
)

func TestNormalizeToolResultContentJSON(t *testing.T) {
	res := ipc.ToolResult{
		Name:   "read_file",
		Output: `{"type":"file_segment","content":"abc"}`,
	}
	out := normalizeToolResultContent(res)

	var got map[string]interface{}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("expected normalized JSON output, got err=%v output=%s", err, out)
	}
	if got["type"] != "tool_result" {
		t.Fatalf("expected type=tool_result, got %+v", got)
	}
	if got["tool"] != "read_file" {
		t.Fatalf("expected tool=read_file, got %+v", got)
	}
	if got["ok"] != true {
		t.Fatalf("expected ok=true, got %+v", got)
	}
	if got["is_json"] != true {
		t.Fatalf("expected is_json=true, got %+v", got)
	}
	payload, ok := got["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected payload object, got %+v", got)
	}
	if payload["type"] != "file_segment" {
		t.Fatalf("expected payload.type=file_segment, got %+v", payload)
	}
}

func TestNormalizeToolResultContentTextAndError(t *testing.T) {
	okRes := ipc.ToolResult{Name: "bash", Output: "line1\nline2"}
	okOut := normalizeToolResultContent(okRes)
	var okGot map[string]interface{}
	if err := json.Unmarshal([]byte(okOut), &okGot); err != nil {
		t.Fatalf("expected normalized JSON for text output, got err=%v", err)
	}
	if okGot["is_json"] != false {
		t.Fatalf("expected is_json=false, got %+v", okGot)
	}
	if okGot["text"] != "line1\nline2" {
		t.Fatalf("expected text payload, got %+v", okGot)
	}

	errRes := ipc.ToolResult{Name: "read_file", Output: "permission denied", IsError: true}
	errOut := normalizeToolResultContent(errRes)
	var errGot map[string]interface{}
	if err := json.Unmarshal([]byte(errOut), &errGot); err != nil {
		t.Fatalf("expected normalized JSON for error output, got err=%v", err)
	}
	if errGot["ok"] != false {
		t.Fatalf("expected ok=false, got %+v", errGot)
	}
	if errGot["error"] != "permission denied" {
		t.Fatalf("expected error field, got %+v", errGot)
	}
}
