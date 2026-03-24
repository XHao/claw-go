package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// defaultClassifyTimeout caps the time Phase 2 (LLM classification) may spend
// before gracefully degrading to the task tier.  This bounds the worst-case
// first-token latency added by the auto-router when the routing model is slow.
const defaultClassifyTimeout = 5 * time.Second

// RouteDecision is the output of AutoRouter.Classify.
type RouteDecision struct {
	Hint       ModelHint
	Reason     string  // e.g. "heuristic:keyword", "heuristic:simple", "heuristic:skill", "llm:thinking", "llm:task"
	ReasonCode string  // machine-readable reason label
	Confidence float64 // 0..1 estimate when available
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
	classifyTimeout  time.Duration
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
		classifyTimeout:  defaultClassifyTimeout,
	}
}

// Classify determines the appropriate model tier for the current turn.
// text is the latest user message; history is the full session history
// (already including text as the last user message); skillNames is the list
// of available server-side skills/tools.
func (a *AutoRouter) Classify(ctx context.Context, text string, history []Message, skillNames []string) RouteDecision {
	if matchThinkingKeyword(text, a.thinkingKeywords) {
		return RouteDecision{Hint: ModelHintThinking, Reason: "heuristic:keyword", ReasonCode: "keyword_match", Confidence: 0.99}
	}
	if heuristicSimple(text) {
		return RouteDecision{Hint: ModelHintTask, Reason: "heuristic:simple", ReasonCode: "simple_message", Confidence: 0.95}
	}
	if canHandleBySkill(text, skillNames) {
		return RouteDecision{Hint: ModelHintTask, Reason: "heuristic:skill", ReasonCode: "skill_match", Confidence: 0.95}
	}
	return a.llmClassify(ctx, history)
}

// autoRouteSystemPrompt is sent to the cheap routing-tier model.
// Keep it minimal and machine-readable.
const autoRouteSystemPrompt = `You are a task routing classifier for an AI coding assistant.
Analyze the conversation and respond with ONLY valid JSON — no explanation, no markdown.

Route to "thinking" ONLY when the task clearly requires:
- Multi-file refactoring or large-scale code architecture design
- Deep debugging that traces through multiple interacting systems
- Complex trade-off analysis or system/API design decisions
- Performance optimization requiring deep algorithmic analysis

Route to "task" for everything else: questions, explanations, simple code, short commands, translations.

Respond ONLY with JSON using this schema:
{"tier":"task|thinking","reason_code":"short_snake_case","confidence":0.0}`

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
	// withNoFallback prevents a 429 or transient error on the routing model from
	// silently escalating to the expensive task model via FallbackProvider.
	// The short deadline bounds the worst-case TTFT impact of Phase 2.
	classifyCtx := withNoFallback(WithModelHint(ctx, ModelHintRouter))
	classifyCtx = WithHintSource(classifyCtx, HintSourceAutorouteClassify)
	classifyCtx, cancel := context.WithTimeout(classifyCtx, a.classifyTimeout)
	defer cancel()

	reply, err := a.provider.Complete(classifyCtx, msgs)
	if err != nil {
		// Classification failure is non-fatal: fall back to task tier.
		return RouteDecision{Hint: ModelHintTask, Reason: "llm:error", ReasonCode: "classifier_error", Confidence: 0}
	}

	// Parse — strip any accidental markdown fences.
	clean := strings.TrimSpace(reply)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	var result struct {
		Tier       string  `json:"tier"`
		ReasonCode string  `json:"reason_code"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(clean), &result); err != nil {
		return RouteDecision{Hint: ModelHintTask, Reason: "llm:parse_error", ReasonCode: "parse_error", Confidence: 0}
	}
	if result.Confidence < 0 {
		result.Confidence = 0
	}
	if result.Confidence > 1 {
		result.Confidence = 1
	}
	if strings.TrimSpace(result.ReasonCode) == "" {
		result.ReasonCode = "classifier_output"
	}
	if result.Tier == "thinking" {
		return RouteDecision{Hint: ModelHintThinking, Reason: "llm:thinking", ReasonCode: result.ReasonCode, Confidence: result.Confidence}
	}
	return RouteDecision{Hint: ModelHintTask, Reason: "llm:task", ReasonCode: result.ReasonCode, Confidence: result.Confidence}
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
