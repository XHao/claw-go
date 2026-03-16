package client

import (
	"fmt"
	"strings"
	"time"

	"github.com/XHao/claw-go/ipc"
)

type usageSample struct {
	ts     time.Time
	model  string
	hint   string
	tokens int
}

type modelAgg struct {
	calls  int
	tokens int
}

// UsageTracker keeps cumulative totals plus a 60-second sliding window
// for realtime RPM/TPM rendering in the CLI.
type UsageTracker struct {
	samples     []usageSample
	totalCalls  int
	totalTokens int
	byModel     map[string]modelAgg
	last        *ipc.LLMUsageEvent
}

func NewUsageTracker() *UsageTracker {
	return &UsageTracker{byModel: make(map[string]modelAgg)}
}

func (u *UsageTracker) Add(ev ipc.LLMUsageEvent) {
	now := time.Now()
	u.last = &ev
	u.totalCalls++
	u.totalTokens += ev.TotalTokens

	model := formatModel(ev.ModelKey, ev.Model)
	agg := u.byModel[model]
	agg.calls++
	agg.tokens += ev.TotalTokens
	u.byModel[model] = agg

	hint := strings.TrimSpace(ev.Hint)
	if hint == "" {
		hint = "task"
	}
	u.samples = append(u.samples, usageSample{ts: now, model: model, hint: hint, tokens: ev.TotalTokens})
	u.prune(now)
}

func (u *UsageTracker) SpinnerLabel() string {
	if u.last == nil {
		return ""
	}
	calls, tokens, _, _ := u.windowStats(time.Now())
	return fmt.Sprintf("RPM:%d TPM:%d", calls, tokens)
}

func (u *UsageTracker) SummaryLine() string {
	if u.last == nil {
		return ""
	}
	now := time.Now()
	calls, tokens, byModel, byHint := u.windowStats(now)
	last := u.last
	lastModel := formatModel(last.ModelKey, last.Model)
	modelWinCalls, modelWinTokens := byModel[lastModel].calls, byModel[lastModel].tokens
	route := byHint["router"]
	mainCalls := calls - route.calls
	mainTokens := tokens - route.tokens
	return strings.Join([]string{
		fmt.Sprintf("1m RPM=%d TPM=%d", calls, tokens),
		fmt.Sprintf("route=%d/%d main=%d/%d", route.calls, route.tokens, mainCalls, mainTokens),
		fmt.Sprintf("model(%s) 1m=%d/%d", lastModel, modelWinCalls, modelWinTokens),
		fmt.Sprintf("last=%d tok %dms", last.TotalTokens, last.LatencyMs),
		fmt.Sprintf("all=%d calls %d tok", u.totalCalls, u.totalTokens),
	}, "  |  ")
}

func (u *UsageTracker) windowStats(now time.Time) (int, int, map[string]modelAgg, map[string]modelAgg) {
	u.prune(now)
	byModel := make(map[string]modelAgg)
	byHint := make(map[string]modelAgg)
	calls := 0
	tokens := 0
	for _, s := range u.samples {
		calls++
		tokens += s.tokens
		agg := byModel[s.model]
		agg.calls++
		agg.tokens += s.tokens
		byModel[s.model] = agg
		h := byHint[s.hint]
		h.calls++
		h.tokens += s.tokens
		byHint[s.hint] = h
	}
	return calls, tokens, byModel, byHint
}

func (u *UsageTracker) prune(now time.Time) {
	cutoff := now.Add(-time.Minute)
	idx := 0
	for idx < len(u.samples) && u.samples[idx].ts.Before(cutoff) {
		idx++
	}
	if idx > 0 {
		u.samples = append([]usageSample(nil), u.samples[idx:]...)
	}
}

func formatModel(modelKey, model string) string {
	switch {
	case modelKey != "" && model != "":
		return modelKey + "/" + model
	case modelKey != "":
		return modelKey
	case model != "":
		return model
	default:
		return "unknown"
	}
}
