package client

import (
	"strings"
	"testing"

	"github.com/XHao/claw-go/ipc"
)

func TestUsageTrackerAggregatesWindow(t *testing.T) {
	tr := NewUsageTracker()
	tr.Add(ipc.LLMUsageEvent{Hint: "router", ModelKey: "route_model", Model: "gpt-4o-mini", TotalTokens: 120, LatencyMs: 800})
	tr.Add(ipc.LLMUsageEvent{Hint: "task", ModelKey: "task_model", Model: "gpt-4o", TotalTokens: 80, LatencyMs: 600})

	label := tr.SpinnerLabel()
	if !strings.Contains(label, "RPM:2") {
		t.Fatalf("unexpected spinner label: %q", label)
	}
	if !strings.Contains(label, "TPM:200") {
		t.Fatalf("unexpected spinner label: %q", label)
	}

	summary := tr.SummaryLine()
	if !strings.Contains(summary, "1m RPM=2 TPM=200") {
		t.Fatalf("unexpected summary line: %q", summary)
	}
	if !strings.Contains(summary, "route=1/120 main=1/80") {
		t.Fatalf("unexpected summary line: %q", summary)
	}
	if !strings.Contains(summary, "all=2 calls 200 tok") {
		t.Fatalf("unexpected summary line: %q", summary)
	}
}
