package provider

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// RouteDecision is the output of AutoRouter.Classify.
type RouteDecision struct {
	Hint   ModelHint
	Reason string // e.g. "heuristic:keyword", "heuristic:simple", "heuristic:skill", "llm:thinking", "llm:task"
}

// AutoRouter classifies each incoming user message into a model tier before
// the main LLM loop runs.  This lets the system automatically select the
// thinking tier for complex tasks without the user typing /think.
//
// Two-phase design:
//
//	Phase 1 (zero cost, ~microseconds): local heuristics
//	  – obvious simple text → task immediately
//	  – skill-name match    → task immediately
//	Phase 2 (cheap LLM, ~200ms): routes ambiguous messages through the
//	  routing-tier model (ModelHintRouter) for a structured classification.
//
// The AutoRouter does NOT know which physical model backs each tier; it just
// attaches a ModelHint to the context.  Any Provider (including RouterProvider)
// can be passed as the underlying provider.
type AutoRouter struct {
	provider         Provider
	thinkingKeywords []string
}

// DefaultThinkingKeywords are built-in fast-path triggers for complex requests.
var DefaultThinkingKeywords = []string{
	"帮我规划",
	"help me plan",
	"plan this out",
	"给个方案",
	"give me a plan",
	"give me a proposal",
	"分步骤",
	"step by step",
	"break it down",
	"怎么权衡",
	"how to balance",
	"how to trade off",
	"利弊",
	"pros and cons",
	"trade-offs",
	"深入分析",
	"deep analysis",
	"analyze in depth",
	"根因",
	"root cause",
	"root cause analysis",
	"重构",
	"refactor",
	"refactoring",
	"架构",
	"architecture",
	"architecture design",
}

// NewAutoRouter creates an AutoRouter backed by provider.
// For the routing to be cost-effective, provider should be (or wrap) a
// RouterProvider so that classification calls — which carry ModelHintRouter —
// are dispatched to the cheapest routing-tier model.
func NewAutoRouter(p Provider, extraThinkingKeywords []string) *AutoRouter {
	return &AutoRouter{
		provider:         p,
		thinkingKeywords: mergeKeywords(DefaultThinkingKeywords, extraThinkingKeywords),
	}
}

// Classify determines the appropriate model tier for the current turn.
// text is the latest user message; history is the full session history
// (already including text as the last user message); skillNames is the list
// of available server-side skills/tools.
func (a *AutoRouter) Classify(ctx context.Context, text string, history []Message, skillNames []string) RouteDecision {
	if matchThinkingKeyword(text, a.thinkingKeywords) {
		return RouteDecision{Hint: ModelHintThinking, Reason: "heuristic:keyword"}
	}
	if heuristicSimple(text) {
		return RouteDecision{Hint: ModelHintTask, Reason: "heuristic:simple"}
	}
	if canHandleBySkill(text, skillNames) {
		return RouteDecision{Hint: ModelHintTask, Reason: "heuristic:skill"}
	}
	return a.llmClassify(ctx, history)
}

// autoRouteSystemPrompt is sent to the cheap routing-tier model.
// Keep it minimal — the classifier only outputs {"tier":"task"} or {"tier":"thinking"}.
const autoRouteSystemPrompt = `You are a task routing classifier for an AI coding assistant.
Analyze the conversation and respond with ONLY valid JSON — no explanation, no markdown.

Route to "thinking" ONLY when the task clearly requires:
- Multi-file refactoring or large-scale code architecture design
- Deep debugging that traces through multiple interacting systems
- Complex trade-off analysis or system/API design decisions
- Performance optimization requiring deep algorithmic analysis

Route to "task" for everything else: questions, explanations, simple code, short commands, translations.

Respond ONLY with: {"tier":"task"} or {"tier":"thinking"}`

// llmClassify sends the last few conversation turns to the routing-tier model
// and parses its decision.
func (a *AutoRouter) llmClassify(ctx context.Context, history []Message) RouteDecision {
	// Extract a compact context: last 3 user/assistant messages (skip tool-only turns).
	var recent []Message
	for i := len(history) - 1; i >= 0 && len(recent) < 3; i-- {
		m := history[i]
		if (m.Role == "user" || m.Role == "assistant") && m.Content != "" {
			recent = append([]Message{m}, recent...)
		}
	}

	msgs := make([]Message, 0, len(recent)+1)
	msgs = append(msgs, Message{Role: "system", Content: autoRouteSystemPrompt})
	msgs = append(msgs, recent...)

	// Route the classifier call through the routing tier (cheapest model).
	classifyCtx := WithModelHint(ctx, ModelHintRouter)
	classifyCtx = WithHintSource(classifyCtx, "autoroute/classify")

	reply, err := a.provider.Complete(classifyCtx, msgs)
	if err != nil {
		// Classification failure is non-fatal: fall back to task tier.
		return RouteDecision{Hint: ModelHintTask, Reason: "llm:error"}
	}

	// Parse — strip any accidental markdown fences.
	clean := strings.TrimSpace(reply)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	var result struct {
		Tier string `json:"tier"`
	}
	if err := json.Unmarshal([]byte(clean), &result); err != nil {
		return RouteDecision{Hint: ModelHintTask, Reason: "llm:parse_error"}
	}
	if result.Tier == "thinking" {
		return RouteDecision{Hint: ModelHintThinking, Reason: "llm:thinking"}
	}
	return RouteDecision{Hint: ModelHintTask, Reason: "llm:task"}
}

// heuristicSimple returns true for messages that are clearly low-complexity
// and do not need even a classifier call.
func heuristicSimple(text string) bool {
	n := utf8.RuneCountInString(text)
	if n < 25 {
		return true
	}
	// Single-line, no question punctuation, short enough
	if n < 60 && !strings.ContainsAny(text, "\n？?") {
		return true
	}
	return false
}

func matchThinkingKeyword(text string, keywords []string) bool {
	if len(keywords) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		k := strings.TrimSpace(strings.ToLower(kw))
		if k != "" && strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func mergeKeywords(base, extra []string) []string {
	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, kw := range list {
			k := strings.TrimSpace(strings.ToLower(kw))
			if k == "" {
				continue
			}
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}

// canHandleBySkill returns true if the message matches any available skill name.
// This routes skill/tool-invocation messages to the fast path (task tier)
// without classifier overhead.
func canHandleBySkill(text string, skillNames []string) bool {
	if len(skillNames) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, name := range skillNames {
		if strings.Contains(lower, strings.ToLower(name)) {
			return true
		}
	}
	return false
}
