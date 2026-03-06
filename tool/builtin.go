package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/XHao/claw-go/provider"
)

// AllDefs returns the ToolDef list for every built-in tool that is enabled.
// Pass the names you wish to expose (nil = all built-ins).
func AllDefs(allowed []string) []provider.ToolDef {
	all := []provider.ToolDef{BashDef, ReadFileDef, WriteFileDef, ListFilesDef}
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
	Description: "Read the contents of a file and return them as a string.",
	Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute or relative path to the file."}},"required":["path"]}`),
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
		Path string `json:"path"`
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
	f, err := os.Open(args.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, limit+1)
	n, _ := f.Read(buf)
	content := string(buf[:n])
	if int64(n) > limit {
		content = content[:limit] + "\n[file truncated]"
	}
	return content, nil
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
