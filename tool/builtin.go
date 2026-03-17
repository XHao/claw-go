package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/XHao/claw-go/provider"
)

// AllDefs returns the ToolDef list for every built-in tool that is enabled.
// Pass the names you wish to expose (nil = all built-ins).
func AllDefs(allowed []string) []provider.ToolDef {
	all := []provider.ToolDef{BashDef, ReadFileDef, GrepFileDef, SearchFileDef, WriteFileDef, ListFilesDef}
	if len(allowed) == 0 {
		return all
	}
	set := make(map[string]bool, len(allowed))
	for _, n := range allowed {
		set[n] = true
	}
	var out []provider.ToolDef
	for _, d := range all {
		if set[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

// ─── Tool definitions (schemas shown to the LLM) ─────────────────────────────

var BashDef = provider.ToolDef{
	Name:        "bash",
	Description: "Execute a shell command and return its combined stdout+stderr output. Prefer short, non-interactive commands. Use for file manipulation, package management, running tests, etc.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"The shell command to execute."}},"required":["command"]}`),
}

var ReadFileDef = provider.ToolDef{
	Name:        "read_file",
	Description: "Read a file as text. Supports optional offset/max_bytes for segmented reads. Large files return a guided preview instead of the entire content.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute or relative path to the file."},"offset":{"type":"integer","description":"Optional byte offset to start reading from. Defaults to 0."},"max_bytes":{"type":"integer","description":"Optional maximum bytes to read for this call. Useful for large-file segmented inspection."}},"required":["path"]}`),
}

var SearchFileDef = provider.ToolDef{
	Name:        "search_file",
	Description: "Search within a file without loading the whole file into the model context. Returns byte offsets and short snippets around each match. Prefer this for large logs, traces, flamegraphs, and minified HTML before using read_file segments.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute or relative path to the file."},"query":{"type":"string","description":"Text to search for inside the file."},"max_matches":{"type":"integer","description":"Maximum number of matches to return. Defaults to 5."},"context_bytes":{"type":"integer","description":"Bytes of surrounding context to include around each match. Defaults to 96."},"case_sensitive":{"type":"boolean","description":"Whether the search is case-sensitive. Defaults to false."}},"required":["path","query"]}`),
}

var GrepFileDef = provider.ToolDef{
	Name:        "grep_file",
	Description: "Search a text file line by line and return matching line numbers. Supports plain substring or regular expression matching. Prefer this for logs, code, configs, stack traces, and other line-oriented text files.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute or relative path to the file."},"query":{"type":"string","description":"Substring or regular expression to search for."},"is_regexp":{"type":"boolean","description":"Interpret query as a regular expression. Defaults to false."},"case_sensitive":{"type":"boolean","description":"Whether the search is case-sensitive. Defaults to false."},"max_matches":{"type":"integer","description":"Maximum number of matching lines to return. Defaults to 20."}},"required":["path","query"]}`),
}

var WriteFileDef = provider.ToolDef{
	Name:        "write_file",
	Description: "Write content to a file, creating or overwriting it. Returns a confirmation message.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to write to."},"content":{"type":"string","description":"File content."}},"required":["path","content"]}`),
}

var ListFilesDef = provider.ToolDef{
	Name:        "list_files",
	Description: "List files and directories in a given path. Returns a newline-separated list.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Directory to list. Defaults to current directory if empty."}}}`),
}

// ─── Local executor ──────────────────────────────────────────────────────────

// LocalRunner executes built-in tools in the client's process environment.
type LocalRunner struct {
	BashTimeoutSeconds int
	AllowedCommands    []string // empty = all allowed
	MaxFileBytes       int64    // 0 = default (100KB)
}

// Run dispatches a tool call to the appropriate implementation.
// argsJSON is the raw JSON argument object from the LLM.
func (r *LocalRunner) Run(ctx context.Context, name, argsJSON string) (output string, isErr bool) {
	switch name {
	case "bash":
		out, err := r.runBash(ctx, argsJSON)
		if err != nil {
			return err.Error(), true
		}
		return out, false
	case "read_file":
		out, err := r.runReadFile(argsJSON)
		if err != nil {
			return err.Error(), true
		}
		return out, false
	case "grep_file":
		out, err := r.runGrepFile(argsJSON)
		if err != nil {
			return err.Error(), true
		}
		return out, false
	case "search_file":
		out, err := r.runSearchFile(argsJSON)
		if err != nil {
			return err.Error(), true
		}
		return out, false
	case "write_file":
		out, err := r.runWriteFile(argsJSON)
		if err != nil {
			return err.Error(), true
		}
		return out, false
	case "list_files":
		out, err := r.runListFiles(argsJSON)
		if err != nil {
			return err.Error(), true
		}
		return out, false
	default:
		return fmt.Sprintf("unknown tool: %q", name), true
	}
}

func (r *LocalRunner) runBash(ctx context.Context, argsJSON string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("bash: parse args: %w", err)
	}
	if args.Command == "" {
		return "", fmt.Errorf("bash: command is empty")
	}

	// Allowlist check.
	if len(r.AllowedCommands) > 0 {
		first := strings.Fields(args.Command)
		if len(first) > 0 {
			base := filepath.Base(first[0])
			allowed := false
			for _, a := range r.AllowedCommands {
				if a == base {
					allowed = true
					break
				}
			}
			if !allowed {
				return "", fmt.Errorf("bash: command %q is not in the allowed list", base)
			}
		}
	}

	timeout := 30 * time.Second
	if r.BashTimeoutSeconds > 0 {
		timeout = time.Duration(r.BashTimeoutSeconds) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.CommandContext(runCtx, shell, "-c", args.Command)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err := cmd.Run()
	out := buf.String()
	if err != nil {
		// Return combined output + error; LLM can decide how to handle it.
		if out == "" {
			out = err.Error()
		} else {
			out = out + "\n[exit error: " + err.Error() + "]"
		}
	}
	const maxOut = 32 * 1024 // 32KB cap to avoid giant context
	if len(out) > maxOut {
		out = out[:maxOut] + "\n[output truncated]"
	}
	return out, nil
}

func (r *LocalRunner) runReadFile(argsJSON string) (string, error) {
	var args struct {
		Path     string `json:"path"`
		Offset   int64  `json:"offset"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("read_file: parse args: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("read_file: path is empty")
	}
	limit := r.MaxFileBytes
	if limit <= 0 {
		limit = 100 * 1024
	}
	if args.Offset < 0 {
		return "", fmt.Errorf("read_file: offset must be >= 0")
	}
	readBytes := args.MaxBytes
	if readBytes <= 0 || readBytes > limit {
		readBytes = limit
	}
	info, err := os.Stat(args.Path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("read_file: path is a directory")
	}
	f, err := os.Open(args.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// If the caller did not request a segment and the file is large, return a guided preview.
	if args.Offset == 0 && args.MaxBytes <= 0 && info.Size() > limit {
		return previewLargeFile(f, args.Path, info.Size(), limit)
	}
	return readFileSegment(f, args.Path, info.Size(), args.Offset, readBytes)
}

func previewLargeFile(f *os.File, path string, size, limit int64) (string, error) {
	const previewHead = 16 * 1024
	const previewTail = 6 * 1024
	headBytes := minInt64(previewHead, limit/2)
	if headBytes <= 0 {
		headBytes = minInt64(4*1024, limit)
	}
	tailBytes := minInt64(previewTail, limit-headBytes)
	if tailBytes < 0 {
		tailBytes = 0
	}

	head, err := readAtMost(f, 0, headBytes)
	if err != nil {
		return "", err
	}
	tail := ""
	if tailBytes > 0 && size > headBytes {
		start := size - tailBytes
		if start < headBytes {
			start = headBytes
		}
		if start < size {
			tailChunk, err := readAtMost(f, start, size-start)
			if err != nil {
				return "", err
			}
			tail = tailChunk
		}
	}

	var sb strings.Builder
	sb.WriteString("[large file preview]\n")
	sb.WriteString(fmt.Sprintf("path: %s\n", path))
	sb.WriteString(fmt.Sprintf("size: %d bytes\n", size))
	sb.WriteString(fmt.Sprintf("default_read_limit: %d bytes\n", limit))
	sb.WriteString("note: full file was not returned because it exceeds the safe read limit.\n")
	sb.WriteString("hint: use grep_file {path, query} for line-oriented text, or search_file {path, query} for minified/HTML-like content, then read_file {path, offset, max_bytes} for targeted inspection.\n")
	if looksLikeFlamegraph(path, head) {
		sb.WriteString("hint: this looks like a flamegraph/html trace; search for keywords such as translog, fsync, flush, write, sync before reading segments.\n")
	}
	sb.WriteString("\n--- BEGIN HEAD PREVIEW ---\n")
	sb.WriteString(head)
	sb.WriteString("\n--- END HEAD PREVIEW ---\n")
	if tail != "" {
		sb.WriteString(fmt.Sprintf("\n--- BEGIN TAIL PREVIEW (offset=%d) ---\n", size-int64(len(tail))))
		sb.WriteString(tail)
		sb.WriteString("\n--- END TAIL PREVIEW ---\n")
	}
	return sb.String(), nil
}

func readFileSegment(f *os.File, path string, size, offset, maxBytes int64) (string, error) {
	if offset > size {
		return "", fmt.Errorf("read_file: offset %d exceeds file size %d", offset, size)
	}
	chunk, err := readAtMost(f, offset, maxBytes)
	if err != nil {
		return "", err
	}
	end := offset + int64(len(chunk))
	var sb strings.Builder
	if offset > 0 || end < size {
		sb.WriteString("[file segment]\n")
		sb.WriteString(fmt.Sprintf("path: %s\n", path))
		sb.WriteString(fmt.Sprintf("range: [%d, %d) of %d bytes\n", offset, end, size))
		sb.WriteString("\n")
	}
	sb.WriteString(chunk)
	if end < size {
		sb.WriteString(fmt.Sprintf("\n[file truncated, next_offset=%d]", end))
	}
	return sb.String(), nil
}

func readAtMost(f *os.File, offset, maxBytes int64) (string, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	buf := make([]byte, maxBytes)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return "", err
	}
	return string(buf[:n]), nil
}

func looksLikeFlamegraph(path, head string) bool {
	lowerPath := strings.ToLower(path)
	lowerHead := strings.ToLower(head)
	return strings.HasSuffix(lowerPath, ".html") &&
		(strings.Contains(lowerHead, "flamegraph") || strings.Contains(lowerHead, "<svg") || strings.Contains(lowerHead, "search") || strings.Contains(lowerHead, "zoom"))
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func (r *LocalRunner) runGrepFile(argsJSON string) (string, error) {
	var args struct {
		Path          string `json:"path"`
		Query         string `json:"query"`
		IsRegexp      bool   `json:"is_regexp"`
		CaseSensitive bool   `json:"case_sensitive"`
		MaxMatches    int    `json:"max_matches"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("grep_file: parse args: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("grep_file: path is empty")
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("grep_file: query is empty")
	}
	if args.MaxMatches <= 0 {
		args.MaxMatches = 20
	}
	if args.MaxMatches > 100 {
		args.MaxMatches = 100
	}

	info, err := os.Stat(args.Path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("grep_file: path is a directory")
	}
	data, err := os.ReadFile(args.Path)
	if err != nil {
		return "", err
	}
	matcher, err := compileLineMatcher(args.Query, args.IsRegexp, args.CaseSensitive)
	if err != nil {
		return "", err
	}
	return grepLines(data, args.Path, args.Query, args.IsRegexp, args.CaseSensitive, args.MaxMatches, matcher), nil
}

func compileLineMatcher(query string, isRegexp, caseSensitive bool) (func(string) bool, error) {
	if !isRegexp {
		if caseSensitive {
			return func(line string) bool { return strings.Contains(line, query) }, nil
		}
		lowerQuery := strings.ToLower(query)
		return func(line string) bool { return strings.Contains(strings.ToLower(line), lowerQuery) }, nil
	}
	pattern := query
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("grep_file: invalid regexp: %w", err)
	}
	return re.MatchString, nil
}

func grepLines(data []byte, path, query string, isRegexp, caseSensitive bool, maxMatches int, match func(string) bool) string {
	var sb strings.Builder
	sb.WriteString("[file grep]\n")
	sb.WriteString(fmt.Sprintf("path: %s\n", path))
	sb.WriteString(fmt.Sprintf("size: %d bytes\n", len(data)))
	sb.WriteString(fmt.Sprintf("query: %q\n", query))
	mode := "substring"
	if isRegexp {
		mode = "regexp"
	}
	sb.WriteString(fmt.Sprintf("mode: %s\n", mode))
	sb.WriteString(fmt.Sprintf("case_sensitive: %t\n", caseSensitive))

	lineNo := 1
	count := 0
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		if match(line) {
			count++
			if count == 1 {
				sb.WriteString("matches:\n")
			}
			sb.WriteString(fmt.Sprintf("- L%d: %s\n", lineNo, truncateSearchLine(line, 240)))
			if count >= maxMatches {
				break
			}
		}
		lineNo++
	}
	if count == 0 {
		sb.WriteString("matches: 0\n")
		sb.WriteString("note: no matching lines found.\n")
		return sb.String()
	}
	if count >= maxMatches {
		sb.WriteString("[note] match limit reached; refine query or increase max_matches if needed.\n")
	}
	return sb.String()
}

func truncateSearchLine(line string, max int) string {
	if len(line) <= max {
		return line
	}
	if max <= 3 {
		return line[:max]
	}
	return line[:max-3] + "..."
}

func (r *LocalRunner) runSearchFile(argsJSON string) (string, error) {
	var args struct {
		Path          string `json:"path"`
		Query         string `json:"query"`
		MaxMatches    int    `json:"max_matches"`
		ContextBytes  int    `json:"context_bytes"`
		CaseSensitive bool   `json:"case_sensitive"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("search_file: parse args: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("search_file: path is empty")
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("search_file: query is empty")
	}
	if args.MaxMatches <= 0 {
		args.MaxMatches = 5
	}
	if args.MaxMatches > 20 {
		args.MaxMatches = 20
	}
	if args.ContextBytes <= 0 {
		args.ContextBytes = 96
	}
	if args.ContextBytes > 512 {
		args.ContextBytes = 512
	}

	info, err := os.Stat(args.Path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("search_file: path is a directory")
	}
	f, err := os.Open(args.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	matches, err := searchFileChunks(f, args.Query, args.MaxMatches, args.ContextBytes, args.CaseSensitive)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("[file search]\n")
	sb.WriteString(fmt.Sprintf("path: %s\n", args.Path))
	sb.WriteString(fmt.Sprintf("size: %d bytes\n", info.Size()))
	sb.WriteString(fmt.Sprintf("query: %q\n", args.Query))
	sb.WriteString(fmt.Sprintf("matches: %d\n", len(matches)))
	if len(matches) == 0 {
		sb.WriteString("note: no matches found.\n")
		return sb.String(), nil
	}
	for i, m := range matches {
		sb.WriteString(fmt.Sprintf("\n[%d] offset=%d\n", i+1, m.offset))
		sb.WriteString(m.snippet)
		sb.WriteString("\n")
	}
	if len(matches) == args.MaxMatches {
		sb.WriteString("\n[note] match limit reached; refine query or increase max_matches if needed.\n")
	}
	return sb.String(), nil
}

type fileMatch struct {
	offset  int64
	snippet string
}

func searchFileChunks(f *os.File, query string, maxMatches, contextBytes int, caseSensitive bool) ([]fileMatch, error) {
	needle := []byte(query)
	if !caseSensitive {
		needle = []byte(strings.ToLower(query))
	}
	if len(needle) == 0 {
		return nil, nil
	}

	reader := bufio.NewReaderSize(f, 32*1024)
	chunkSize := 32 * 1024
	overlap := len(needle) + contextBytes*2
	if overlap < len(needle)+32 {
		overlap = len(needle) + 32
	}

	var (
		results    []fileMatch
		baseOffset int64
		carryRaw   []byte
		carryCmp   []byte
		lastOffset int64 = -1
	)

	for len(results) < maxMatches {
		chunk := make([]byte, chunkSize)
		n, err := io.ReadAtLeast(reader, chunk, 1)
		if err != nil {
			if err == io.EOF {
				break
			}
			if err == io.ErrUnexpectedEOF || err == bufio.ErrBufferFull {
				if n == 0 {
					break
				}
			} else {
				return nil, err
			}
		}
		chunk = chunk[:n]
		cmpChunk := chunk
		if !caseSensitive {
			cmpChunk = []byte(strings.ToLower(string(chunk)))
		}

		rawData := append(append([]byte(nil), carryRaw...), chunk...)
		cmpData := append(append([]byte(nil), carryCmp...), cmpChunk...)
		windowBase := baseOffset - int64(len(carryRaw))

		searchFrom := 0
		for len(results) < maxMatches {
			idx := bytes.Index(cmpData[searchFrom:], needle)
			if idx < 0 {
				break
			}
			idx += searchFrom
			matchOffset := windowBase + int64(idx)
			if matchOffset != lastOffset {
				start := idx - contextBytes
				if start < 0 {
					start = 0
				}
				end := idx + len(needle) + contextBytes
				if end > len(rawData) {
					end = len(rawData)
				}
				snippet := sanitizeSnippet(string(rawData[start:end]))
				results = append(results, fileMatch{offset: matchOffset, snippet: snippet})
				lastOffset = matchOffset
			}
			searchFrom = idx + len(needle)
		}

		if len(rawData) > overlap {
			carryRaw = append([]byte(nil), rawData[len(rawData)-overlap:]...)
			carryCmp = append([]byte(nil), cmpData[len(cmpData)-overlap:]...)
		} else {
			carryRaw = append([]byte(nil), rawData...)
			carryCmp = append([]byte(nil), cmpData...)
		}
		baseOffset += int64(len(chunk))
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			break
		}
	}
	return results, nil
}

func sanitizeSnippet(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}

func (r *LocalRunner) runWriteFile(argsJSON string) (string, error) {
	var args struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("write_file: parse args: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("write_file: path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(args.Path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(args.Path, []byte(args.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(args.Content), args.Path), nil
}

func (r *LocalRunner) runListFiles(argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	json.Unmarshal([]byte(argsJSON), &args) // ignore parse errors; path defaults to ""
	dir := args.Path
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, e := range entries {
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		lines = append(lines, e.Name()+suffix)
	}
	return strings.Join(lines, "\n"), nil
}
