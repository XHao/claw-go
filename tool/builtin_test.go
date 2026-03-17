package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
	if !strings.Contains(out, "[large file preview]") {
		t.Fatalf("expected large file preview, got: %s", out)
	}
	if !strings.Contains(out, "grep_file {path, query}") {
		t.Fatalf("expected grep_file hint, got: %s", out)
	}
	if !strings.Contains(out, "translog") {
		t.Fatalf("expected preview content to include file text, got: %s", out)
	}
	if !strings.Contains(out, "flamegraph/html trace") {
		t.Fatalf("expected flamegraph-specific hint, got: %s", out)
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
	if !strings.Contains(out, "[file segment]") {
		t.Fatalf("expected file segment header, got: %s", out)
	}
	if !strings.Contains(out, "abcdefgh") {
		t.Fatalf("expected requested segment content, got: %s", out)
	}
	if !strings.Contains(out, "next_offset=18") {
		t.Fatalf("expected next_offset marker, got: %s", out)
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
	args, _ := json.Marshal(map[string]any{"path": path, "query": "translog", "max_matches": 2})
	out, err := r.runSearchFile(string(args))
	if err != nil {
		t.Fatalf("runSearchFile error: %v", err)
	}
	if !strings.Contains(out, "[file search]") {
		t.Fatalf("expected file search header, got: %s", out)
	}
	if !strings.Contains(out, `query: "translog"`) {
		t.Fatalf("expected query in output, got: %s", out)
	}
	if !strings.Contains(out, "offset=") {
		t.Fatalf("expected byte offsets, got: %s", out)
	}
	if !strings.Contains(out, "translog fsync") {
		t.Fatalf("expected snippet around match, got: %s", out)
	}
	if !strings.Contains(out, "match limit reached") {
		t.Fatalf("expected limit note, got: %s", out)
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
	if !strings.Contains(out, "matches: 1") {
		t.Fatalf("expected one match, got: %s", out)
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
	if !strings.Contains(out, "[file grep]") {
		t.Fatalf("expected grep header, got: %s", out)
	}
	if !strings.Contains(out, "mode: substring") {
		t.Fatalf("expected substring mode, got: %s", out)
	}
	if !strings.Contains(out, "L2: WARN translog fsync slow") {
		t.Fatalf("expected matching line with number, got: %s", out)
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
	if !strings.Contains(out, "mode: regexp") {
		t.Fatalf("expected regexp mode, got: %s", out)
	}
	if !strings.Contains(out, "L2: beta translog_123") {
		t.Fatalf("expected regexp match, got: %s", out)
	}
	if !strings.Contains(out, "match limit reached") {
		t.Fatalf("expected match limit note, got: %s", out)
	}
}
