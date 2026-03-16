package client

import (
	"strings"
	"testing"

	"github.com/XHao/claw-go/ipc"
)

func TestUsageTrackerAggregatesWindow(t *testing.T) {
	tr := NewUsageTracker()
	tr.BeginTurn()
	tr.Add(ipc.LLMUsageEvent{Hint: "router", ModelKey: "route_model", Model: "gpt-4o-mini", PromptTokens: 100, CompletionTokens: 20, InputTokensEst: 30, ContextTokensEst: 70, TotalTokens: 120, LatencyMs: 800})
	tr.Add(ipc.LLMUsageEvent{Hint: "task", ModelKey: "task_model", Model: "gpt-4o", PromptTokens: 60, CompletionTokens: 20, InputTokensEst: 15, ContextTokensEst: 45, TotalTokens: 80, LatencyMs: 600})

	label := tr.SpinnerLabel()
	if !strings.Contains(label, "tok=200") {
		t.Fatalf("unexpected spinner label: %q", label)
	}

	summary := tr.TurnSummaryLine()
	if !strings.Contains(summary, "turn calls=2 tok=200") {
		t.Fatalf("unexpected summary line: %q", summary)
	}
	if !strings.Contains(summary, "latency_max=800ms err=0") {
		t.Fatalf("unexpected summary line: %q", summary)
	}
	if !strings.Contains(summary, "route=1/120 main=1/80") {
		t.Fatalf("unexpected summary line: %q", summary)
	}

	report := strings.Join(tr.PrettyReportLines(), "\n")
	if !strings.Contains(report, "窗口(1m): RPM=2 TPM=200") {
		t.Fatalf("unexpected report: %q", report)
	}
	compact := strings.Join(tr.CompactReportLines(), "\n")
	if !strings.Contains(compact, "1m: rpm=2 tpm=200") {
		t.Fatalf("unexpected compact report: %q", compact)
	}
	if !strings.Contains(report, "累计(本次 CLI): calls=2 tok=200") {
		t.Fatalf("unexpected report: %q", report)
	}

	tr.Reset()
	if got := tr.TurnSummaryLine(); got != "" {
		t.Fatalf("expected empty turn summary after reset, got %q", got)
	}
	report2 := strings.Join(tr.PrettyReportLines(), "\n")
	if !strings.Contains(report2, "暂无 token 统计数据") {
		t.Fatalf("unexpected report after reset: %q", report2)
	}
}
