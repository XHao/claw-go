package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// DebugProvider wraps any Provider and writes full LLM request/response
// traces to a dedicated file. This makes it easy to audit exactly what
// messages and tools are sent to the model and what it decided to call.
//
// Format (human-readable text blocks):
//
//	═══════════════════════════════ LLM 调用 #N ════════════
//	  工具列表 (K 个)
//	  消息历史 (M 条)
//	  → 响应: stop_reason / tool_calls / error
//	═══════════════════════════════════════════════════════
type DebugProvider struct {
	inner   Provider
	out     io.WriteCloser
	callSeq atomic.Int64
}

// WrapDebug wraps inner with debug logging that appends to filePath.
// If the file cannot be opened, it prints a warning to stderr and returns
// inner unchanged (never panics, never blocks startup).
func WrapDebug(inner Provider, filePath string) Provider {
	// Make sure the parent directory exists.
	_ = os.MkdirAll(filepath.Dir(filePath), 0o700)
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "debug provider: cannot open %q: %v\n", filePath, err)
		return inner
	}
	return &DebugProvider{inner: inner, out: f}
}

// Complete implements Provider.
func (d *DebugProvider) Complete(ctx context.Context, messages []Message) (string, error) {
	result, err := d.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// CompleteWithTools implements Provider; logs the full exchange to the debug file.
func (d *DebugProvider) CompleteWithTools(ctx context.Context, messages []Message, tools []ToolDef) (CompleteResult, error) {
	seq := d.callSeq.Add(1)
	w := d.out
	ts := time.Now().Format("2006-01-02 15:04:05.000")
	bar := strings.Repeat("═", 72)
	thin := strings.Repeat("─", 68)

	// ── HEADER ──────────────────────────────────────────────────────────
	fmt.Fprintf(w, "\n%s\n", bar)
	fmt.Fprintf(w, "  LLM 调用 #%d   %s\n", seq, ts)
	fmt.Fprintf(w, "%s\n", bar)

	// ── TOOLS ────────────────────────────────────────────────────────────
	if len(tools) > 0 {
		fmt.Fprintf(w, "\n── 可用工具 (%d 个) %s\n", len(tools), thin)
		for i, t := range tools {
			desc := t.Description
			if len([]rune(desc)) > 80 {
				desc = string([]rune(desc)[:77]) + "..."
			}
			fmt.Fprintf(w, "  [%d] %-30s  %s\n", i+1, t.Name, desc)
		}
	}

	// ── MESSAGES ─────────────────────────────────────────────────────────
	fmt.Fprintf(w, "\n── 消息历史 (%d 条) %s\n", len(messages), thin)
	for i, m := range messages {
		roleLine := strings.ToUpper(m.Role)
		if m.Name != "" {
			roleLine += " (" + m.Name + ")"
		}
		if m.ToolCallID != "" {
			roleLine += " tool_call_id=" + m.ToolCallID
		}
		fmt.Fprintf(w, "\n[%d] %s\n", i+1, roleLine)

		if m.Content != "" {
			content := m.Content
			// Truncate very long content (e.g. injected experience docs).
			const maxContent = 3000
			if len(content) > maxContent {
				content = content[:maxContent] + fmt.Sprintf("\n... [截断，共 %d 字符] ...", len(m.Content))
			}
			for _, line := range strings.Split(content, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}

		for _, tc := range m.ToolCalls {
			fmt.Fprintf(w, "    → TOOL_CALL  id=%-36s  name=%s\n", tc.ID, tc.Function.Name)
			args := prettyArgs(tc.Function.Arguments)
			for _, line := range strings.Split(args, "\n") {
				fmt.Fprintf(w, "      %s\n", line)
			}
		}
	}

	// ── CALL ─────────────────────────────────────────────────────────────
	hint := HintFromContext(ctx)
	if hint == "" {
		hint = ModelHintTask
	}
	source := SourceFromContext(ctx)
	sourceStr := ""
	if source != "" {
		sourceStr = "  source=" + source
	}
	fmt.Fprintf(w, "\n── 请求发送 → 等待 LLM 响应  [hint=%s%s] %s\n", hint, sourceStr, thin)
	start := time.Now()
	result, err := d.inner.CompleteWithTools(ctx, messages, tools)
	elapsed := time.Since(start).Milliseconds()

	// ── RESPONSE ─────────────────────────────────────────────────────────
	if err != nil {
		fmt.Fprintf(w, "\n── ❌ 错误  elapsed=%dms %s\n", elapsed, thin)
		fmt.Fprintf(w, "    %v\n", err)
		fmt.Fprintf(w, "\n%s\n", bar)
		return result, err
	}

	modelStr := ""
	switch {
	case result.Model.ModelKey != "" && result.Model.Model != "":
		modelStr = fmt.Sprintf("  model=%s/%s", result.Model.ModelKey, result.Model.Model)
	case result.Model.ModelKey != "":
		modelStr = fmt.Sprintf("  model=%s", result.Model.ModelKey)
	case result.Model.Model != "":
		modelStr = fmt.Sprintf("  model=%s", result.Model.Model)
	}
	fmt.Fprintf(w, "\n── ✅ 响应  stop_reason=%-16s  elapsed=%dms  tokens=%d(%d+%d)%s %s\n",
		result.StopReason, elapsed,
		result.Usage.TotalTokens, result.Usage.PromptTokens, result.Usage.CompletionTokens,
		modelStr,
		thin)

	if result.Content != "" {
		content := result.Content
		const maxReply = 3000
		if len(content) > maxReply {
			content = content[:maxReply] + fmt.Sprintf("\n... [截断，共 %d 字符] ...", len(result.Content))
		}
		for _, line := range strings.Split(content, "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}

	for _, tc := range result.ToolCalls {
		fmt.Fprintf(w, "\n    ▶ TOOL CALL: %s  (id=%s)\n", tc.Function.Name, tc.ID)
		args := prettyArgs(tc.Function.Arguments)
		for _, line := range strings.Split(args, "\n") {
			fmt.Fprintf(w, "      %s\n", line)
		}
	}

	fmt.Fprintf(w, "\n%s\n", bar)
	return result, nil
}

// prettyArgs tries to pretty-print JSON arguments; falls back to raw string.
func prettyArgs(raw string) string {
	if raw == "" {
		return "(no args)"
	}
	var v interface{}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	b, err := json.MarshalIndent(v, "      ", "  ")
	if err != nil {
		return raw
	}
	return string(b)
}
