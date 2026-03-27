package tool

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
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
	all := []provider.ToolDef{BashDef, InspectFileDef, ReadFileDef, SearchFileDef, WriteFileDef, ListFilesDef}
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

var InspectFileDef = provider.ToolDef{
	Name:        "inspect_file",
	Description: "Inspect a file before any content read. Returns metadata, text/binary heuristics, format hints, and a recommended analysis strategy. This should usually be the first step for unknown, large, binary, Office, archive, flamegraph, HTML, or generated files.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute or relative path to the file."}},"required":["path"]}`),
}

var ReadFileDef = provider.ToolDef{
	Name:        "read_file",
	Description: "Read a file as text. Supports optional offset/max_bytes for segmented reads. Do not use this as the first step for unknown or large files: inspect_file first, then search_file to locate relevant regions, and finally use read_file for targeted segments.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute or relative path to the file."},"offset":{"type":"integer","description":"Optional byte offset to start reading from. Defaults to 0."},"max_bytes":{"type":"integer","description":"Optional maximum bytes to read for this call. Useful for large-file segmented inspection."}},"required":["path"]}`),
}

var SearchFileDef = provider.ToolDef{
	Name:        "search_file",
	Description: `Search within a file. mode="line" (default): line-by-line scan returning matching line numbers and content snippets — best for source code, logs, configs, stack traces; supports is_regexp. mode="byte": byte-offset scan returning snippets with byte positions — best for HTML, flamegraphs, minified assets, dense traces. Run inspect_file first for unknown or binary-like files.`,
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute or relative path to the file."},"query":{"type":"string","description":"Text or pattern to search for."},"mode":{"type":"string","enum":["line","byte"],"description":"Search mode: \"line\" for line-by-line match (default), \"byte\" for byte-offset match."},"is_regexp":{"type":"boolean","description":"Treat query as a regular expression (line mode only). Defaults to false."},"case_sensitive":{"type":"boolean","description":"Case-sensitive search. Defaults to false."},"max_matches":{"type":"integer","description":"Maximum matches to return. Defaults to 20 (line mode) or 5 (byte mode)."},"context_bytes":{"type":"integer","description":"Bytes of surrounding context per match (byte mode only). Defaults to 96."}},"required":["path","query"]}`),
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

// RunContext carries per-invocation metadata that extensible tool handlers may
// need: the client's working directory (for shell/file tools) and the session
// key (for tools that operate on daemon-internal state such as memory).
type RunContext struct {
	Cwd        string
	SessionKey string
}

type toolExec func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error)

// ExtHandler is the function signature for all registered (non-builtin) tools.
// argsJSON is the raw JSON argument object from the LLM; handlers should
// unmarshal it into a typed struct to preserve all parameter types (strings,
// integers, booleans, etc.).
// rctx carries the calling context (cwd, sessionKey).
// progress delivers live status lines to the caller.
type ExtHandler func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error)

type extEntry struct {
	def     provider.ToolDef
	exec    toolExec
	group   string // tool group: "core" (always included) or "knowledge" (on-demand)
	builtin bool
}

// LocalRunner executes built-in tools in the daemon process.
type LocalRunner struct {
	BashTimeoutSeconds int
	AllowedCommands    []string // empty = all shell commands allowed (bash tool)
	AllowedTools       []string // empty = all built-in tools exposed to LLM
	MaxFileBytes       int64    // 0 = default (100KB)

	handlers map[string]extEntry // registered extension tools
}

// BuiltinDefs returns the ToolDef slice for the built-in tools that are
// enabled for this runner, honoring the AllowedTools allowlist.
func (r *LocalRunner) BuiltinDefs() []provider.ToolDef {
	return AllDefs(r.AllowedTools)
}

// Register adds an extensible tool to the runner under the "core" group.
// Registered tools cannot override builtins. Safe to call before or after
// SetToolRunner in agent.
func (r *LocalRunner) Register(def provider.ToolDef, fn ExtHandler) {
	r.RegisterGroup("core", def, fn)
}

// RegisterGroup adds an extensible tool with an explicit group tag.
// "core" tools are always included in the LLM tool list; other groups (e.g.
// "knowledge") are included on demand based on the agent's intent detection.
func (r *LocalRunner) RegisterGroup(group string, def provider.ToolDef, fn ExtHandler) {
	r.ensureBuiltinHandlers()
	if group == "" {
		group = "core"
	}
	if existing, ok := r.handlers[def.Name]; ok && existing.builtin {
		return
	}
	r.handlers[def.Name] = extEntry{
		def:   def,
		group: group,
		exec:  toolExec(fn),
	}
}

// RegisteredDefs returns the ToolDef slice for all registered (non-builtin)
// tools, across all groups.
func (r *LocalRunner) RegisteredDefs() []provider.ToolDef {
	return r.RegisteredDefsByGroup()
}

// RegisteredDefsByGroup returns the ToolDef slice for registered tools that
// belong to any of the specified groups. If no groups are given, all registered
// tools are returned.
func (r *LocalRunner) RegisteredDefsByGroup(groups ...string) []provider.ToolDef {
	r.ensureBuiltinHandlers()
	if len(r.handlers) == 0 {
		return nil
	}
	var filter map[string]bool
	if len(groups) > 0 {
		filter = make(map[string]bool, len(groups))
		for _, g := range groups {
			filter[g] = true
		}
	}
	out := make([]provider.ToolDef, 0, len(r.handlers))
	for _, e := range r.handlers {
		if e.builtin {
			continue
		}
		if filter == nil || filter[e.group] {
			out = append(out, e.def)
		}
	}
	return out
}

func (r *LocalRunner) ensureBuiltinHandlers() {
	if r.handlers == nil {
		r.handlers = make(map[string]extEntry)
	}
	r.registerBuiltin(BashDef, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		return r.runBash(ctx, argsJSON, rctx.Cwd)
	})
	r.registerBuiltin(InspectFileDef, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		return r.runInspectFile(argsJSON)
	})
	r.registerBuiltin(ReadFileDef, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		return r.runReadFile(argsJSON)
	})
	r.registerBuiltin(SearchFileDef, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		return r.runSearchFile(argsJSON)
	})
	r.registerBuiltin(WriteFileDef, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		return r.runWriteFile(argsJSON)
	})
	r.registerBuiltin(ListFilesDef, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		return r.runListFiles(argsJSON)
	})
}

func (r *LocalRunner) registerBuiltin(def provider.ToolDef, exec toolExec) {
	if _, exists := r.handlers[def.Name]; exists {
		return
	}
	r.handlers[def.Name] = extEntry{def: def, exec: exec, group: "core", builtin: true}
}

// Run dispatches a tool call to the appropriate implementation.
// argsJSON is the raw JSON argument object from the LLM.
// rctx carries the client's working directory and session key.
// progress delivers live status updates to the caller; may be nil.
func (r *LocalRunner) Run(ctx context.Context, name, argsJSON string, rctx RunContext, progress func(string)) (output string, isErr bool) {
	r.ensureBuiltinHandlers()
	if progress == nil {
		progress = func(string) {}
	}
	e, ok := r.handlers[name]
	if !ok {
		return fmt.Sprintf("unknown tool: %q", name), true
	}
	out, err := e.exec(ctx, argsJSON, rctx, progress)
	if err != nil {
		return err.Error(), true
	}
	return out, false
}

func (r *LocalRunner) runBash(ctx context.Context, argsJSON, cwd string) (string, error) {
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
	if cwd != "" {
		cmd.Dir = cwd
	}
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
	ext := strings.ToLower(filepath.Ext(args.Path))
	sample, err := readAtMost(f, 0, minInt64(8*1024, info.Size()))
	if err != nil {
		return "", err
	}
	if msg, blocked := blockTextToolUse(args.Path, ext, sample); blocked {
		return msg, nil
	}

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

	type previewReport struct {
		Type             string   `json:"type"`
		Path             string   `json:"path"`
		Size             int64    `json:"size"`
		DefaultReadLimit int64    `json:"default_read_limit"`
		RecommendedNext  string   `json:"recommended_next"`
		RiskLevel        string   `json:"risk_level"`
		Hints            []string `json:"hints,omitempty"`
		HeadPreview      string   `json:"head_preview"`
		TailPreview      string   `json:"tail_preview,omitempty"`
	}
	hints := []string{"Use inspect_file first when the format is unclear", "For text files, use search_file before read_file segments"}
	if looksLikeFlamegraph(path, head) {
		hints = append(hints, "Flamegraph/HTML trace detected: search for translog, fsync, flush, write, sync before reading segments")
	}
	report := previewReport{
		Type:             "large_file_preview",
		Path:             path,
		Size:             size,
		DefaultReadLimit: limit,
		RecommendedNext:  "inspect_file -> search_file -> read_file(segment)",
		RiskLevel:        "medium",
		Hints:            hints,
		HeadPreview:      head,
		TailPreview:      tail,
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("preview_large_file: marshal report: %w", err)
	}
	return string(b), nil
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
	type segmentReport struct {
		Type       string `json:"type"`
		Path       string `json:"path"`
		Size       int64  `json:"size"`
		RangeStart int64  `json:"range_start"`
		RangeEnd   int64  `json:"range_end"`
		Truncated  bool   `json:"truncated"`
		NextOffset int64  `json:"next_offset,omitempty"`
		Content    string `json:"content"`
	}
	report := segmentReport{
		Type:       "file_segment",
		Path:       path,
		Size:       size,
		RangeStart: offset,
		RangeEnd:   end,
		Truncated:  end < size,
		Content:    chunk,
	}
	if end < size {
		report.NextOffset = end
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("read_file: marshal segment report: %w", err)
	}
	return string(b), nil
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

func blockTextToolUse(path, ext, sample string) (string, bool) {
	type guardReport struct {
		Type                string `json:"type"`
		Path                string `json:"path"`
		RiskLevel           string `json:"risk_level"`
		Reason              string `json:"reason"`
		RecommendedStrategy string `json:"recommended_strategy"`
	}
	if isOfficeContainer(ext) {
		report := guardReport{
			Type:                "tool_guard",
			Path:                path,
			RiskLevel:           "high",
			Reason:              fmt.Sprintf("Office container detected (%s); plain-text reads are unsafe and low-value", ext),
			RecommendedStrategy: "inspect_file -> metadata_only_or_specialized_extractor",
		}
		b, _ := json.MarshalIndent(report, "", "  ")
		return string(b), true
	}
	if looksBinary(sample) {
		report := guardReport{
			Type:                "tool_guard",
			Path:                path,
			RiskLevel:           "high",
			Reason:              "Binary-like content detected; avoid direct text search/read tools",
			RecommendedStrategy: "inspect_file -> metadata_only",
		}
		b, _ := json.MarshalIndent(report, "", "  ")
		return string(b), true
	}
	return "", false
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

func (r *LocalRunner) runInspectFile(argsJSON string) (string, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("inspect_file: parse args: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("inspect_file: path is empty")
	}
	info, err := os.Stat(args.Path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("inspect_file: path is a directory")
	}
	f, err := os.Open(args.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	sampleSize := minInt64(32*1024, info.Size())
	sample, err := readAtMost(f, 0, sampleSize)
	if err != nil {
		return "", err
	}

	ext := strings.ToLower(filepath.Ext(args.Path))
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		mimeType = detectMimeFromContent(ext, sample)
	}
	binary := looksBinary(sample)
	lang := detectLanguageHint(ext)
	structure := detectStructure(args.Path, sample, binary)
	recommended, rationale, risk := recommendStrategy(ext, mimeType, binary, structure, lang)
	hints := []string{}
	if isOfficeContainer(ext) {
		hints = append(hints, "Office container detected: prefer specialized extraction or export to text/CSV/Markdown")
	}
	if binary {
		hints = append(hints, "Binary-like content detected: avoid direct text read/search tools")
	}
	if structure == "flamegraph_html" {
		hints = append(hints, "keywords: translog, fsync, flush, write, sync")
	}
	type inspectReport struct {
		Type                string   `json:"type"`
		Path                string   `json:"path"`
		Size                int64    `json:"size"`
		Extension           string   `json:"extension"`
		MIME                string   `json:"mime"`
		IsBinary            bool     `json:"is_binary"`
		LanguageHint        string   `json:"language_hint"`
		Structure           string   `json:"structure"`
		RecommendedStrategy string   `json:"recommended_strategy"`
		RiskLevel           string   `json:"risk_level"`
		Rationale           string   `json:"rationale"`
		Hints               []string `json:"hints,omitempty"`
	}
	report := inspectReport{
		Type:                "file_inspect",
		Path:                args.Path,
		Size:                info.Size(),
		Extension:           emptyAsUnknown(ext),
		MIME:                emptyAsUnknown(mimeType),
		IsBinary:            binary,
		LanguageHint:        lang,
		Structure:           structure,
		RecommendedStrategy: recommended,
		RiskLevel:           risk,
		Rationale:           rationale,
		Hints:               hints,
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("inspect_file: marshal report: %w", err)
	}
	return string(b), nil
}

func detectMimeFromContent(ext, sample string) string {
	lower := strings.ToLower(sample)
	switch {
	case strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype html"):
		return "text/html"
	case strings.Contains(lower, "<svg"):
		return "image/svg+xml"
	case strings.HasSuffix(ext, ".json"):
		return "application/json"
	case strings.HasSuffix(ext, ".yaml") || strings.HasSuffix(ext, ".yml"):
		return "application/yaml"
	default:
		return "application/octet-stream"
	}
}

func looksBinary(sample string) bool {
	if sample == "" {
		return false
	}
	data := []byte(sample)
	nul := bytes.Count(data, []byte{0})
	if nul > 0 {
		return true
	}
	bad := 0
	for _, b := range data {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 0x20 || b == 0x7f {
			bad++
		}
	}
	return len(data) > 0 && float64(bad)/float64(len(data)) > 0.10
}

func detectLanguageHint(ext string) string {
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".java":
		return "java"
	case ".rb":
		return "ruby"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp":
		return "cpp"
	case ".sh", ".bash", ".zsh":
		return "shell"
	case ".json":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".xml":
		return "xml"
	case ".log", ".txt", ".md":
		return "plain_text"
	default:
		return "unknown"
	}
}

func detectStructure(path, sample string, binary bool) string {
	if binary {
		if isOfficeContainer(strings.ToLower(filepath.Ext(path))) {
			return "office_container"
		}
		return "binary"
	}
	if looksLikeFlamegraph(path, sample) {
		return "flamegraph_html"
	}
	lineCount := 1 + strings.Count(sample, "\n")
	maxLine := 0
	for _, line := range strings.Split(sample, "\n") {
		if len(line) > maxLine {
			maxLine = len(line)
		}
	}
	if lineCount <= 3 && maxLine > 400 {
		return "minified_text"
	}
	if strings.Contains(strings.ToLower(sample), "<html") || strings.Contains(strings.ToLower(sample), "<svg") {
		return "html"
	}
	if maxLine > 200 {
		return "dense_text"
	}
	return "line_text"
}

func recommendStrategy(ext, mimeType string, binary bool, structure string, lang string) (string, string, string) {
	if isOfficeContainer(ext) {
		return "metadata_only_or_specialized_extractor", "container document format; plain-text reading is low-value and misleading", "high"
	}
	if binary {
		return "metadata_only", "binary-like content should not be sent directly to text-oriented tools", "high"
	}
	if structure == "flamegraph_html" || structure == "minified_text" || strings.Contains(mimeType, "html") {
		return "search_file -> read_file(segment)", "large or minified markup is better searched by byte offset before targeted reads", "medium"
	}
	if lang != "unknown" && lang != "plain_text" && lang != "html" && lang != "json" && lang != "yaml" {
		return "search_file -> read_file(segment)", "source code is line-oriented and usually best explored via line matches first", "low"
	}
	return "search_file -> read_file(segment)", "line-oriented text is best narrowed by matching lines before detailed reads", "low"
}

func isOfficeContainer(ext string) bool {
	switch ext {
	case ".docx", ".xlsx", ".pptx":
		return true
	default:
		return false
	}
}

func emptyAsUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
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
		return "", fmt.Errorf("search_file: parse args: %w", err)
	}
	if args.Path == "" {
		return "", fmt.Errorf("search_file: path is empty")
	}
	if strings.TrimSpace(args.Query) == "" {
		return "", fmt.Errorf("search_file: query is empty")
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
		return "", fmt.Errorf("search_file: path is a directory")
	}
	f, err := os.Open(args.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	ext := strings.ToLower(filepath.Ext(args.Path))
	sample, err := readAtMost(f, 0, minInt64(8*1024, info.Size()))
	if err != nil {
		return "", err
	}
	if msg, blocked := blockTextToolUse(args.Path, ext, sample); blocked {
		return msg, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	matcher, err := compileLineMatcher(args.Query, args.IsRegexp, args.CaseSensitive)
	if err != nil {
		return "", err
	}
	return grepFileLines(f, info.Size(), args.Path, args.Query, args.IsRegexp, args.CaseSensitive, args.MaxMatches, matcher)
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
		return nil, fmt.Errorf("search_file: invalid regexp: %w", err)
	}
	return re.MatchString, nil
}

func grepFileLines(r io.Reader, size int64, path, query string, isRegexp, caseSensitive bool, maxMatches int, match func(string) bool) (string, error) {
	mode := "substring"
	if isRegexp {
		mode = "regexp"
	}
	type grepMatch struct {
		Line    int    `json:"line"`
		Snippet string `json:"snippet"`
	}
	type grepReport struct {
		Type          string      `json:"type"`
		Path          string      `json:"path"`
		Size          int64       `json:"size"`
		Query         string      `json:"query"`
		Mode          string      `json:"mode"`
		CaseSensitive bool        `json:"case_sensitive"`
		Matches       []grepMatch `json:"matches"`
		Truncated     bool        `json:"truncated"`
		Note          string      `json:"note,omitempty"`
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 1
	report := grepReport{
		Type:          "file_search",
		Path:          path,
		Size:          size,
		Query:         query,
		Mode:          mode,
		CaseSensitive: caseSensitive,
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if match(line) {
			report.Matches = append(report.Matches, grepMatch{Line: lineNo, Snippet: truncateSearchLine(line, 240)})
			if len(report.Matches) >= maxMatches {
				report.Truncated = true
				break
			}
		}
		lineNo++
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if len(report.Matches) == 0 {
		report.Note = "no matching lines found"
	}
	if report.Truncated {
		report.Note = "match limit reached; refine query or increase max_matches if needed"
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("search_file: marshal report: %w", err)
	}
	return string(b), nil
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

// runSearchFile dispatches to line-mode (grep-style) or byte-offset search based
// on the mode parameter. mode="byte" → runSearchFileByte; anything else → runGrepFile.
func (r *LocalRunner) runSearchFile(argsJSON string) (string, error) {
	var modeOnly struct {
		Mode string `json:"mode"`
	}
	_ = json.Unmarshal([]byte(argsJSON), &modeOnly)
	if modeOnly.Mode == "byte" {
		return r.runSearchFileByte(argsJSON)
	}
	return r.runGrepFile(argsJSON)
}

func (r *LocalRunner) runSearchFileByte(argsJSON string) (string, error) {
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
	ext := strings.ToLower(filepath.Ext(args.Path))
	sample, err := readAtMost(f, 0, minInt64(8*1024, info.Size()))
	if err != nil {
		return "", err
	}
	if msg, blocked := blockTextToolUse(args.Path, ext, sample); blocked {
		return msg, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	matches, err := searchFileChunks(f, args.Query, args.MaxMatches, args.ContextBytes, args.CaseSensitive)
	if err != nil {
		return "", err
	}
	type searchMatch struct {
		Offset  int64  `json:"offset"`
		Snippet string `json:"snippet"`
	}
	type searchReport struct {
		Type          string        `json:"type"`
		Path          string        `json:"path"`
		Size          int64         `json:"size"`
		Query         string        `json:"query"`
		CaseSensitive bool          `json:"case_sensitive"`
		Matches       []searchMatch `json:"matches"`
		Truncated     bool          `json:"truncated"`
		Note          string        `json:"note,omitempty"`
	}
	report := searchReport{
		Type:          "file_search",
		Path:          args.Path,
		Size:          info.Size(),
		Query:         args.Query,
		CaseSensitive: args.CaseSensitive,
	}
	for _, m := range matches {
		report.Matches = append(report.Matches, searchMatch{Offset: m.offset, Snippet: m.snippet})
	}
	if len(matches) == 0 {
		report.Note = "no matches found"
	}
	if len(matches) == args.MaxMatches {
		report.Truncated = true
		report.Note = "match limit reached; refine query or increase max_matches if needed"
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("search_file: marshal report: %w", err)
	}
	return string(b), nil
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
