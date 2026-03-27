package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/XHao/claw-go/provider"
)

func decodeInspectReport(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("inspect output is not valid JSON: %v\noutput=%s", err, out)
	}
	return m
}

func decodeJSONReport(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput=%s", err, out)
	}
	return m
}

func TestRunReadFileLargeFileReturnsPreview(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "flamegraph.html")
	content := strings.Repeat("<svg>flamegraph translog sample</svg>\n", 400)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{MaxFileBytes: 8 * 1024}
	args, _ := json.Marshal(map[string]any{"path": path})
	out, err := r.runReadFile(string(args))
	if err != nil {
		t.Fatalf("runReadFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	if report["type"] != "large_file_preview" {
		t.Fatalf("expected large_file_preview, got: %+v", report)
	}
	if report["recommended_next"] == nil {
		t.Fatalf("expected recommended_next, got: %+v", report)
	}
	if !strings.Contains(report["head_preview"].(string), "translog") {
		t.Fatalf("expected preview content to include file text, got: %+v", report)
	}
}

func TestRegisterCannotOverrideBuiltin(t *testing.T) {
	r := &LocalRunner{}
	r.Register(provider.ToolDef{Name: "list_files"}, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		return "overridden", nil
	})

	args, _ := json.Marshal(map[string]any{"path": t.TempDir()})
	out, isErr := r.Run(context.Background(), "list_files", string(args), RunContext{}, nil)
	if isErr {
		t.Fatalf("expected builtin list_files to run successfully, got error: %s", out)
	}
	if out == "overridden" {
		t.Fatalf("expected builtin tool to remain active, got registered override output")
	}
}

func TestRunReadFileSegment(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sample.txt")
	content := "0123456789abcdefghijklmnopqrstuvwxyz"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{MaxFileBytes: 64}
	args, _ := json.Marshal(map[string]any{"path": path, "offset": 10, "max_bytes": 8})
	out, err := r.runReadFile(string(args))
	if err != nil {
		t.Fatalf("runReadFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	if report["type"] != "file_segment" {
		t.Fatalf("expected file_segment output, got: %+v", report)
	}
	if int64(report["range_start"].(float64)) != 10 || int64(report["range_end"].(float64)) != 18 {
		t.Fatalf("unexpected segment range, got: %+v", report)
	}
	if report["content"] != "abcdefgh" {
		t.Fatalf("expected requested segment content, got: %+v", report)
	}
	if int64(report["next_offset"].(float64)) != 18 {
		t.Fatalf("expected next_offset marker, got: %+v", report)
	}
}

func TestRunSearchFileFindsMatches(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "flamegraph.html")
	content := strings.Repeat("abc translog fsync def ", 300)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path, "query": "translog", "max_matches": 2, "mode": "byte"})
	out, err := r.runSearchFile(string(args))
	if err != nil {
		t.Fatalf("runSearchFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	if report["type"] != "file_search" {
		t.Fatalf("expected file_search, got: %+v", report)
	}
	if report["query"] != "translog" {
		t.Fatalf("expected query in output, got: %+v", report)
	}
	matches, _ := report["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("expected matches, got: %+v", report)
	}
	first, _ := matches[0].(map[string]any)
	if _, ok := first["offset"]; !ok {
		t.Fatalf("expected byte offsets, got: %+v", report)
	}
	if !strings.Contains(first["snippet"].(string), "translog fsync") {
		t.Fatalf("expected snippet around match, got: %+v", report)
	}
	if report["truncated"] != true {
		t.Fatalf("expected truncation flag, got: %+v", report)
	}
}

func TestRunSearchFileCaseInsensitiveByDefault(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sample.txt")
	if err := os.WriteFile(path, []byte("hello TRANSLOG world"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path, "query": "translog"})
	out, err := r.runSearchFile(string(args))
	if err != nil {
		t.Fatalf("runSearchFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	matches, _ := report["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("expected one match, got: %+v", report)
	}
}

func TestRunGrepFileSubstring(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "app.log")
	content := strings.Join([]string{
		"INFO start",
		"WARN translog fsync slow",
		"INFO done",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path, "query": "translog"})
	out, err := r.runGrepFile(string(args))
	if err != nil {
		t.Fatalf("runGrepFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	if report["type"] != "file_search" || report["mode"] != "substring" {
		t.Fatalf("expected grep report, got: %+v", report)
	}
	matches, _ := report["matches"].([]any)
	if len(matches) == 0 {
		t.Fatalf("expected matches, got: %+v", report)
	}
	first, _ := matches[0].(map[string]any)
	if int(first["line"].(float64)) != 2 || first["snippet"].(string) != "WARN translog fsync slow" {
		t.Fatalf("expected matching line with number, got: %+v", report)
	}
}

func TestRunGrepFileRegexp(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "app.log")
	content := strings.Join([]string{
		"alpha",
		"beta translog_123",
		"gamma translog_456",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path, "query": `translog_\d+`, "is_regexp": true, "max_matches": 1})
	out, err := r.runGrepFile(string(args))
	if err != nil {
		t.Fatalf("runGrepFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	if report["mode"] != "regexp" {
		t.Fatalf("expected regexp mode, got: %+v", report)
	}
	matches, _ := report["matches"].([]any)
	first, _ := matches[0].(map[string]any)
	if first["snippet"].(string) != "beta translog_123" {
		t.Fatalf("expected regexp match, got: %+v", report)
	}
	if report["truncated"] != true {
		t.Fatalf("expected truncation flag, got: %+v", report)
	}
}

func TestRunInspectFileForLogText(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.log")
	content := strings.Join([]string{
		"INFO start",
		"WARN translog slow",
		"INFO end",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path})
	out, err := r.runInspectFile(string(args))
	if err != nil {
		t.Fatalf("runInspectFile error: %v", err)
	}
	report := decodeInspectReport(t, out)
	if report["type"] != "file_inspect" {
		t.Fatalf("unexpected report type: %+v", report)
	}
	if report["is_binary"] != false {
		t.Fatalf("expected text classification, got: %+v", report)
	}
	if report["recommended_strategy"] != "search_file -> read_file(segment)" {
		t.Fatalf("expected grep strategy, got: %+v", report)
	}
}

func TestRunInspectFileForFlamegraphHTML(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "flamegraph.html")
	content := strings.Repeat("<html><body><svg>flamegraph translog write sync</svg></body></html>", 100)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path})
	out, err := r.runInspectFile(string(args))
	if err != nil {
		t.Fatalf("runInspectFile error: %v", err)
	}
	report := decodeInspectReport(t, out)
	if report["structure"] != "flamegraph_html" {
		t.Fatalf("expected flamegraph_html structure, got: %+v", report)
	}
	if report["recommended_strategy"] != "search_file -> read_file(segment)" {
		t.Fatalf("expected search strategy, got: %+v", report)
	}
	hints, _ := report["hints"].([]any)
	joined := ""
	for _, h := range hints {
		if s, ok := h.(string); ok {
			joined += s + "\n"
		}
	}
	if !strings.Contains(joined, "translog") {
		t.Fatalf("expected keyword hints, got: %+v", report)
	}
}

func TestRunInspectFileForBinary(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sample.bin")
	content := []byte{0x00, 0x01, 0x02, 0x03, 0xff, 0x00, 0x10}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path})
	out, err := r.runInspectFile(string(args))
	if err != nil {
		t.Fatalf("runInspectFile error: %v", err)
	}
	report := decodeInspectReport(t, out)
	if report["is_binary"] != true {
		t.Fatalf("expected binary classification, got: %+v", report)
	}
	if report["recommended_strategy"] != "metadata_only" {
		t.Fatalf("expected metadata_only strategy, got: %+v", report)
	}
	if report["risk_level"] != "high" {
		t.Fatalf("expected high risk level, got: %+v", report)
	}
}

func TestRunReadFileBlocksBinary(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sample.bin")
	content := []byte{0x00, 0x01, 0x02, 0x03, 0xff, 0x00, 0x10}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{MaxFileBytes: 64}
	args, _ := json.Marshal(map[string]any{"path": path})
	out, err := r.runReadFile(string(args))
	if err != nil {
		t.Fatalf("runReadFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	if report["type"] != "tool_guard" {
		t.Fatalf("expected tool_guard output, got: %+v", report)
	}
	if report["recommended_strategy"] != "inspect_file -> metadata_only" {
		t.Fatalf("expected inspect_file guidance, got: %+v", report)
	}
}

func TestRunGrepFileBlocksOfficeContainer(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "slides.pptx")
	if err := os.WriteFile(path, []byte("pretend office zip payload"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path, "query": "slide"})
	out, err := r.runGrepFile(string(args))
	if err != nil {
		t.Fatalf("runGrepFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	if !strings.Contains(report["reason"].(string), "Office container") {
		t.Fatalf("expected Office guard, got: %+v", report)
	}
	if report["recommended_strategy"] != "inspect_file -> metadata_only_or_specialized_extractor" {
		t.Fatalf("expected extractor guidance, got: %+v", report)
	}
}

func TestRunSearchFileBlocksBinary(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "trace.bin")
	content := []byte{0x00, 0x00, 0xff, 0x10, 0x11, 0x12}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	r := &LocalRunner{}
	args, _ := json.Marshal(map[string]any{"path": path, "query": "translog"})
	out, err := r.runSearchFile(string(args))
	if err != nil {
		t.Fatalf("runSearchFile error: %v", err)
	}
	report := decodeJSONReport(t, out)
	if !strings.Contains(report["reason"].(string), "Binary-like content detected") {
		t.Fatalf("expected binary guard, got: %+v", report)
	}
}
