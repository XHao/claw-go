package client

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/XHao/claw-go/ipc"
)

type usageSample struct {
	ts         time.Time
	model      string
	hint       string
	input      int
	context    int
	prompt     int
	completion int
	tokens     int
	latencyMs  int64
	isError    bool
}

type modelAgg struct {
	calls      int
	input      int
	context    int
	prompt     int
	completion int
	tokens     int
	maxLatency int64
	errors     int
}

// UsageTracker keeps cumulative totals plus a 60-second sliding window
// for realtime RPM/TPM rendering in the CLI.
type UsageTracker struct {
	samples     []usageSample
	totalCalls  int
	totalTokens int
	totalInput  int
	totalCtx    int
	totalPrompt int
	totalReply  int
	byModel     map[string]modelAgg
	last        *ipc.LLMUsageEvent
	turn        modelAgg
	turnByModel map[string]modelAgg
	turnByHint  map[string]modelAgg
}

const (
	turnWarnTokens     = 8000
	turnCriticalTokens = 16000
	turnWarnLatencyMs  = 8000
	turnCritLatencyMs  = 15000
)

func NewUsageTracker() *UsageTracker {
	return &UsageTracker{
		byModel:     make(map[string]modelAgg),
		turnByModel: make(map[string]modelAgg),
		turnByHint:  make(map[string]modelAgg),
	}
}

func (u *UsageTracker) BeginTurn() {
	u.turn = modelAgg{}
	u.turnByModel = make(map[string]modelAgg)
	u.turnByHint = make(map[string]modelAgg)
}

func (u *UsageTracker) Reset() {
	u.samples = nil
	u.totalCalls = 0
	u.totalTokens = 0
	u.totalInput = 0
	u.totalCtx = 0
	u.totalPrompt = 0
	u.totalReply = 0
	u.byModel = make(map[string]modelAgg)
	u.last = nil
	u.BeginTurn()
}

func (u *UsageTracker) Add(ev ipc.LLMUsageEvent) {
	now := time.Now()
	u.last = &ev
	u.totalCalls++
	u.totalTokens += ev.TotalTokens
	u.totalInput += ev.InputTokensEst
	u.totalCtx += ev.ContextTokensEst
	u.totalPrompt += ev.PromptTokens
	u.totalReply += ev.CompletionTokens

	model := formatModel(ev.ModelKey, ev.Model)
	hint := strings.TrimSpace(ev.Hint)
	if hint == "" {
		hint = "task"
	}
	s := usageSample{
		ts:         now,
		model:      model,
		hint:       hint,
		input:      ev.InputTokensEst,
		context:    ev.ContextTokensEst,
		prompt:     ev.PromptTokens,
		completion: ev.CompletionTokens,
		tokens:     ev.TotalTokens,
		latencyMs:  ev.LatencyMs,
		isError:    ev.IsError,
	}
	u.samples = append(u.samples, s)
	u.prune(now)

	agg := mergeAgg(u.byModel[model], s)
	u.byModel[model] = agg

	u.turn = mergeAgg(u.turn, s)
	u.turnByModel[model] = mergeAgg(u.turnByModel[model], s)
	u.turnByHint[hint] = mergeAgg(u.turnByHint[hint], s)
}

func (u *UsageTracker) SpinnerLabel() string {
	if u.turn.calls == 0 {
		return ""
	}
	return fmt.Sprintf("this turn tok=%d", u.turn.tokens)
}

func (u *UsageTracker) TurnSummaryLine() string {
	if u.turn.calls == 0 {
		return ""
	}
	route := u.turnByHint["router"]
	mainCalls := u.turn.calls - route.calls
	mainTokens := u.turn.tokens - route.tokens
	ctxPct := pct(u.turn.context, u.turn.prompt)
	replyPct := pct(u.turn.completion, u.turn.tokens)
	return strings.Join([]string{
		fmt.Sprintf("turn calls=%d tok=%d", u.turn.calls, u.turn.tokens),
		fmt.Sprintf("prompt=%d (ctx~%d/%s input~%d) reply=%d/%s", u.turn.prompt, u.turn.context, ctxPct, u.turn.input, u.turn.completion, replyPct),
		fmt.Sprintf("route=%d/%d main=%d/%d", route.calls, route.tokens, mainCalls, mainTokens),
		fmt.Sprintf("latency_max=%dms err=%d", u.turn.maxLatency, u.turn.errors),
	}, "  |  ")
}

func (u *UsageTracker) TurnLevel() string {
	if u.turn.calls == 0 {
		return "normal"
	}
	if u.turn.tokens >= turnCriticalTokens || u.turn.maxLatency >= turnCritLatencyMs || u.turn.errors > 0 {
		return "critical"
	}
	if u.turn.tokens >= turnWarnTokens || u.turn.maxLatency >= turnWarnLatencyMs {
		return "warn"
	}
	return "normal"
}

func (u *UsageTracker) PrettyReportLines() []string {
	if u.last == nil {
		return []string{"暂无 token 统计数据。"}
	}
	calls, tokens, winByModel, winByHint, winAgg := u.windowStats(time.Now())
	routeWin := winByHint["router"]
	mainWinCalls := calls - routeWin.calls
	mainWinTokens := tokens - routeWin.tokens
	ctxPct := pct(winAgg.context, winAgg.prompt)
	replyPct := pct(winAgg.completion, winAgg.tokens)

	lines := []string{
		fmt.Sprintf("窗口(1m): RPM=%d TPM=%d", calls, tokens),
		fmt.Sprintf("窗口拆分: route=%d/%d main=%d/%d", routeWin.calls, routeWin.tokens, mainWinCalls, mainWinTokens),
		fmt.Sprintf("窗口构成: prompt=%d (ctx~%d/%s input~%d) reply=%d/%s", winAgg.prompt, winAgg.context, ctxPct, winAgg.input, winAgg.completion, replyPct),
		fmt.Sprintf("窗口质量: max_latency=%dms err=%d", winAgg.maxLatency, winAgg.errors),
		fmt.Sprintf("本轮: calls=%d tok=%d | prompt=%d (ctx~%d input~%d) reply=%d", u.turn.calls, u.turn.tokens, u.turn.prompt, u.turn.context, u.turn.input, u.turn.completion),
		fmt.Sprintf("累计(本次 CLI): calls=%d tok=%d | prompt=%d (ctx~%d input~%d) reply=%d", u.totalCalls, u.totalTokens, u.totalPrompt, u.totalCtx, u.totalInput, u.totalReply),
		"注: ctx/input 为基于消息长度比例的估算值，仅用于趋势观察。",
	}

	if len(winByModel) > 0 {
		lines = append(lines, "模型(1m):")
		for _, item := range topModels(winByModel, 5) {
			lines = append(lines, fmt.Sprintf("- %s  calls=%d tok=%d prompt=%d reply=%d max_lat=%dms err=%d", item.name, item.agg.calls, item.agg.tokens, item.agg.prompt, item.agg.completion, item.agg.maxLatency, item.agg.errors))
		}
	}
	return lines
}

func (u *UsageTracker) CompactReportLines() []string {
	if u.last == nil {
		return []string{"暂无 token 统计数据。"}
	}
	calls, tokens, winByModel, winByHint, winAgg := u.windowStats(time.Now())
	routeWin := winByHint["router"]
	mainWinCalls := calls - routeWin.calls
	mainWinTokens := tokens - routeWin.tokens
	lines := []string{
		fmt.Sprintf("1m: rpm=%d tpm=%d", calls, tokens),
		fmt.Sprintf("1m split: route=%d/%d main=%d/%d", routeWin.calls, routeWin.tokens, mainWinCalls, mainWinTokens),
		fmt.Sprintf("1m io: prompt=%d ctx~%d input~%d reply=%d", winAgg.prompt, winAgg.context, winAgg.input, winAgg.completion),
		fmt.Sprintf("turn: calls=%d tok=%d lat=%dms err=%d", u.turn.calls, u.turn.tokens, u.turn.maxLatency, u.turn.errors),
		fmt.Sprintf("cli: calls=%d tok=%d", u.totalCalls, u.totalTokens),
	}
	if len(winByModel) > 0 {
		best := topModels(winByModel, 1)
		if len(best) > 0 {
			lines = append(lines, fmt.Sprintf("top: %s %d tok", best[0].name, best[0].agg.tokens))
		}
	}
	return lines
}

func (u *UsageTracker) windowStats(now time.Time) (int, int, map[string]modelAgg, map[string]modelAgg, modelAgg) {
	u.prune(now)
	byModel := make(map[string]modelAgg)
	byHint := make(map[string]modelAgg)
	agg := modelAgg{}
	calls := 0
	tokens := 0
	for _, s := range u.samples {
		calls++
		tokens += s.tokens
		agg = mergeAgg(agg, s)
		byModel[s.model] = mergeAgg(byModel[s.model], s)
		byHint[s.hint] = mergeAgg(byHint[s.hint], s)
	}
	return calls, tokens, byModel, byHint, agg
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

type namedAgg struct {
	name string
	agg  modelAgg
}

func topModels(byModel map[string]modelAgg, n int) []namedAgg {
	items := make([]namedAgg, 0, len(byModel))
	for name, agg := range byModel {
		items = append(items, namedAgg{name: name, agg: agg})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].agg.tokens == items[j].agg.tokens {
			return items[i].name < items[j].name
		}
		return items[i].agg.tokens > items[j].agg.tokens
	})
	if len(items) > n {
		items = items[:n]
	}
	return items
}

func mergeAgg(dst modelAgg, s usageSample) modelAgg {
	dst.calls++
	dst.input += s.input
	dst.context += s.context
	dst.prompt += s.prompt
	dst.completion += s.completion
	dst.tokens += s.tokens
	if s.latencyMs > dst.maxLatency {
		dst.maxLatency = s.latencyMs
	}
	if s.isError {
		dst.errors++
	}
	return dst
}

func pct(num, den int) string {
	if den <= 0 || num <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%d%%", int(float64(num)*100/float64(den)))
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
