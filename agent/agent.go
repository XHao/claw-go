// Package agent implements the core message dispatcher.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/XHao/claw-go/channel"
	"github.com/XHao/claw-go/ipc"
	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
	"github.com/XHao/claw-go/skill"
)

const defaultMaxToolIterations = 10

const fileToolSelectionPrompt = `When analyzing files, prefer a staged workflow instead of reading blindly.

- For unknown, large, binary-like, Office, HTML, flamegraph, minified, or generated files: call inspect_file first.
- Treat inspect_file, tool_guard, large_file_preview, grep_file, search_file, and read_file outputs as machine-readable JSON. Use recommended_strategy, risk_level, structure, is_binary, matches, truncated, content, range_start, and range_end as primary decision fields.
- Tool messages are normalized as a tool_result JSON envelope. Read ok/is_json first, then consume payload for structured tool outputs, text for plain outputs, and error for failures.
- For line-oriented text such as source code, logs, configs, and stack traces: prefer grep_file before read_file.
- For HTML, flamegraphs, minified text, dense traces, or content without meaningful line structure: prefer search_file before read_file.
- Use read_file only after you know the file type or have narrowed to a relevant segment.
- Avoid direct text reads of binary-like or Office container files unless a prior inspection says it is safe.`

type normalizedToolMessage struct {
	Type    string      `json:"type"`
	Tool    string      `json:"tool"`
	OK      bool        `json:"ok"`
	IsJSON  bool        `json:"is_json"`
	Payload interface{} `json:"payload,omitempty"`
	Text    string      `json:"text,omitempty"`
	Error   string      `json:"error,omitempty"`
}

// Agent ties together provider, session store, and channels.
type Agent struct {
	provider      provider.Provider
	sessions      *session.Store
	channels      map[string]channel.Channel
	tools         []provider.ToolDef
	maxIterations int
	memory        *memory.Manager            // optional; nil = memory disabled
	skillRouter   *skill.Router              // optional; nil = skills disabled
	expStore      *knowledge.ExperienceStore // optional; nil = auto-inject disabled
	autoRouter    *provider.AutoRouter       // optional; nil = manual /think only
	autoInjected  sync.Map                   // tracks "sessionKey:topic" → true
	log           *slog.Logger
}

// New creates an Agent.
func New(p provider.Provider, sessions *session.Store, tools []provider.ToolDef, log *slog.Logger) *Agent {
	maxIter := defaultMaxToolIterations
	return &Agent{
		provider:      p,
		sessions:      sessions,
		channels:      make(map[string]channel.Channel),
		tools:         tools,
		maxIterations: maxIter,
		log:           log,
	}
}

// SetMaxIterations overrides the maximum tool-call iterations per message.
func (a *Agent) SetMaxIterations(n int) {
	if n > 0 {
		a.maxIterations = n
	}
}

// SetMemory attaches a memory.Manager so that each completed turn is
// summarised and persisted to ~/.claw/data/memory/{sessionKey}/short/.
// Calling with nil disables memory persistence.
func (a *Agent) SetMemory(m *memory.Manager) {
	a.memory = m
}

// SetSkillRouter attaches a skill.Router so that skill tool calls are
// executed server-side without a client round-trip. When nil (default)
// all tool calls are forwarded to the client via the exchange function.
func (a *Agent) SetSkillRouter(r *skill.Router) {
	a.skillRouter = r
}

// SetExperienceStore attaches an ExperienceStore so that relevant experience
// libraries are automatically injected as context at the start of each turn.
// Calling with nil disables auto-injection.
func (a *Agent) SetExperienceStore(s *knowledge.ExperienceStore) {
	a.expStore = s
}

// SetAutoRouter attaches an AutoRouter so that each incoming message is
// automatically routed to the appropriate model tier (task vs. thinking)
// without requiring the user to prefix messages with /think.
// Calling with nil disables automatic routing.
func (a *Agent) SetAutoRouter(ar *provider.AutoRouter) {
	a.autoRouter = ar
}

// RegisterChannel adds a channel so the agent can dispatch replies through it.
func (a *Agent) RegisterChannel(ch channel.Channel) {
	a.channels[ch.ID()] = ch
}

func prepareMessagesForLLM(history []provider.Message, tools []provider.ToolDef) []provider.Message {
	history = sanitizeHistoryForToolProtocol(history)
	if !hasFileToolWorkflow(tools) {
		return history
	}
	guide := provider.Message{Role: "system", Content: fileToolSelectionPrompt}
	if len(history) == 0 {
		return []provider.Message{guide}
	}
	prepared := make([]provider.Message, 0, len(history)+1)
	prepared = append(prepared, history[0])
	if history[0].Role == "system" {
		prepared = append(prepared, guide)
		prepared = append(prepared, history[1:]...)
		return prepared
	}
	prepared = append(prepared, guide)
	prepared = append(prepared, history[1:]...)
	return prepared
}

func sanitizeHistoryForToolProtocol(history []provider.Message) []provider.Message {
	if len(history) == 0 {
		return history
	}
	out := make([]provider.Message, 0, len(history))
	pendingToolCalls := make(map[string]struct{})

	for _, m := range history {
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				continue
			}
			if _, ok := pendingToolCalls[m.ToolCallID]; !ok {
				continue
			}
			out = append(out, m)
			delete(pendingToolCalls, m.ToolCallID)
			continue
		}

		if len(pendingToolCalls) > 0 {
			pendingToolCalls = make(map[string]struct{})
		}

		out = append(out, m)
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					pendingToolCalls[tc.ID] = struct{}{}
				}
			}
		}
	}

	return out
}

func hasFileToolWorkflow(tools []provider.ToolDef) bool {
	var hasInspect, hasRead, hasGrep, hasSearch bool
	for _, tool := range tools {
		switch tool.Name {
		case "inspect_file":
			hasInspect = true
		case "read_file":
			hasRead = true
		case "grep_file":
			hasGrep = true
		case "search_file":
			hasSearch = true
		}
	}
	return hasInspect && hasRead && (hasGrep || hasSearch)
}

// Dispatch handles a single inbound user message end-to-end.
// exchange is called when the LLM requests tool execution; it may be nil if
// the channel or caller doesn't support tool calls.
func (a *Agent) Dispatch(ctx context.Context, msg channel.InboundMessage, exchange ipc.ToolExchangeFn) {
	log := a.log.With(
		"session_key", msg.SessionKey,
		"channel", msg.ChannelType+":"+msg.ChannelName,
	)
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	ch := a.channels[msg.ChannelType+":"+msg.ChannelName]
	if ch != nil {
		ctx = provider.WithUsageObserver(ctx, func(ev provider.UsageEvent) {
			_ = ch.Send(ctx, channel.OutboundMessage{
				ChatID: msg.ChatID,
				Usage:  toIPCUsageEvent(ev),
			})
		})
	}

	if text == "/reset" || text == "/start" {
		if msg.SessionKey == session.MainSessionKey {
			// Main session is protected: clear history but keep the session alive.
			a.sessions.ClearHistory(msg.SessionKey)
			if a.memory != nil {
				_ = a.memory.ClearSession(msg.SessionKey)
			}
			if ch != nil {
				_ = ch.Send(ctx, channel.OutboundMessage{
					ChatID: msg.ChatID,
					Text:   "主会话历史已清空，会话本身已保留。",
				})
			}
		} else {
			// Regular session: delete entirely (session + memory files).
			a.sessions.Delete(msg.SessionKey)
			if a.memory != nil {
				_ = a.memory.DeleteSession(msg.SessionKey)
			}
			if ch != nil {
				_ = ch.Send(ctx, channel.OutboundMessage{
					ChatID: msg.ChatID,
					Text:   "会话已清空，开始新对话！",
				})
			}
		}
		return
	}

	// /think <message> — ask the deep-reasoning Tier-3 model for this turn only.
	// Strip the prefix and attach ModelHintThinking to ctx so RouterProvider
	// selects the Tier-3 model for every LLM call in this dispatch.
	if strings.HasPrefix(text, "/think ") || text == "/think" {
		text = strings.TrimSpace(strings.TrimPrefix(text, "/think"))
		ctx = provider.WithModelHint(ctx, provider.ModelHintThinking)
		ctx = provider.WithHintSource(ctx, "agent/think")
		if text == "" {
			if ch != nil {
				_ = ch.Send(ctx, channel.OutboundMessage{
					ChatID: msg.ChatID,
					Text:   "用法：/think <你的问题>",
				})
			}
			return
		}
	}

	sess := a.sessions.Get(msg.SessionKey)
	sess.Append(provider.Message{Role: "user", Content: text}, a.sessions.MaxTurns())
	// Capture turn index right after the user message is appended.
	turnN := sess.TurnCount()

	// Auto-inject relevant experience libraries before the first LLM call.
	a.autoInjectExperiences(ctx, msg, ch)

	// Auto-route: classify message complexity and select a model tier automatically.
	// Only fires when no explicit hint is already set (e.g. via /think).
	if a.autoRouter != nil && provider.HintFromContext(ctx) == provider.ModelHintDefault {
		skillNames := []string{}
		if a.skillRouter != nil {
			skillNames = a.skillRouter.Names()
		}
		decision := a.autoRouter.Classify(ctx, text, sess.HistoryForLLM(provider.ModelHintRouter), skillNames)
		ctx = provider.WithModelHint(ctx, decision.Hint)
		log.Info("auto-routed", "hint", decision.Hint, "reason", decision.Reason, "reason_code", decision.ReasonCode, "confidence", decision.Confidence)
	}

	// Accumulate tool actions for the memory summary.
	var turnActions []memory.Action
	for i := 0; i < a.maxIterations; i++ {
		log.Info("calling LLM", "iteration", i)
		start := time.Now()

		// Tag every call with its loop iteration so metrics/debug show "agent/loop[i=N]".
		loopCtx := provider.WithHintSource(ctx, fmt.Sprintf("agent/loop[i=%d]", i))

		// Build the effective tool list: client-side tools + skill defs.
		// Skill defs are always included (they execute server-side regardless
		// of whether a client exchange is available).
		var tools []provider.ToolDef
		if a.skillRouter != nil {
			tools = append(tools, a.skillRouter.AsToolDefs()...)
		}
		if exchange != nil {
			// Only expose client-side tools when there is an exchange.
			tools = append(tools, a.tools...)
		}

		messages := prepareMessagesForLLM(sess.HistoryForLLM(provider.HintFromContext(loopCtx)), tools)
		result, err := a.provider.CompleteWithTools(loopCtx, messages, tools)
		if err != nil {
			log.Error("LLM completion failed", "err", err)
			a.saveTurnMemory(log, msg.SessionKey, turnN, text, "", turnActions, i, true)
			if ch != nil {
				_ = ch.Send(ctx, channel.OutboundMessage{
					ChatID: msg.ChatID,
					Text:   "抱歉，遇到了错误，请重试。",
				})
			}
			return
		}
		a.sessions.ObservePromptUsage(messages, result.Usage.PromptTokens)
		log.Info("LLM response", "elapsed_ms", time.Since(start).Milliseconds(), "stop_reason", result.StopReason)

		// Case 1: LLM wants to call tools (may include server-side skills).
		if len(result.ToolCalls) > 0 {
			// Persist the assistant's tool-call request into history.
			sess.Append(provider.Message{
				Role:      "assistant",
				ToolCalls: result.ToolCalls,
			}, a.sessions.MaxTurns())

			var allResults []ipc.ToolResult
			var clientCalls []ipc.ToolCall

			for _, tc := range result.ToolCalls {
				log.Info("tool call requested", "tool", tc.Function.Name, "call_id", tc.ID)

				if a.skillRouter != nil && a.skillRouter.Has(tc.Function.Name) {
					// ── Server-side skill: execute inline, no client round-trip.
					progress := func(m string) {
						log.Info("skill progress", "skill", tc.Function.Name, "msg", m)
					}
					output, sErr := a.skillRouter.Execute(
						ctx, tc.Function.Name, tc.Function.Arguments, msg.SessionKey, progress,
					)
					isErr := sErr != nil
					if isErr {
						output = sErr.Error()
						log.Warn("skill error", "skill", tc.Function.Name, "err", sErr)
					} else {
						log.Info("skill result", "skill", tc.Function.Name)
					}
					allResults = append(allResults, ipc.ToolResult{
						CallID:  tc.ID,
						Name:    tc.Function.Name,
						Output:  output,
						IsError: isErr,
					})
				} else {
					// ── Client-side tool: batch for exchange.
					clientCalls = append(clientCalls, ipc.ToolCall{
						ID:   tc.ID,
						Name: tc.Function.Name,
						Args: json.RawMessage(tc.Function.Arguments),
					})
				}
			}

			// Execute client-side tool calls via exchange.
			if len(clientCalls) > 0 {
				if exchange != nil {
					clientResults, err := exchange(clientCalls)
					if err != nil {
						log.Error("tool exchange failed", "err", err)
						a.saveTurnMemory(log, msg.SessionKey, turnN, text, "", turnActions, i, true)
						return
					}
					allResults = append(allResults, clientResults...)
				} else {
					// No exchange available — report tools as unavailable.
					for _, call := range clientCalls {
						allResults = append(allResults, ipc.ToolResult{
							CallID:  call.ID,
							Name:    call.Name,
							Output:  "工具不可用：当前上下文无法执行工具调用",
							IsError: true,
						})
					}
				}
			}

			// Append all tool results as "tool" role messages.
			for _, res := range allResults {
				content := normalizeToolResultContent(res)
				log.Info("tool result", "tool", res.Name, "call_id", res.CallID, "is_error", res.IsError)
				sess.Append(provider.Message{
					Role:       "tool",
					Content:    content,
					ToolCallID: res.CallID,
					Name:       res.Name,
				}, a.sessions.MaxTurns())
			}
			// Record tool actions for the memory summary.
			turnActions = append(turnActions, memory.ExtractActions(result.ToolCalls, allResults)...)
			// Continue loop: call LLM again with the tool results.
			continue
		}

		// Case 2: LLM returned a text reply (done).
		if result.Content == "" {
			log.Warn("LLM returned empty content", "stop_reason", result.StopReason, "iteration", i)
		}
		sess.Append(provider.Message{Role: "assistant", Content: result.Content}, a.sessions.MaxTurns())
		a.saveTurnMemory(log, msg.SessionKey, turnN, text, result.Content, turnActions, i, false)
		if ch == nil {
			log.Error("channel not registered", "channel_id", msg.ChannelType+":"+msg.ChannelName)
			return
		}
		if err := ch.Send(ctx, channel.OutboundMessage{
			ChatID: msg.ChatID,
			Text:   result.Content,
		}); err != nil {
			log.Error("send reply failed", "err", err)
		}
		return
	}

	// Safety: exhausted iterations without a final reply.
	log.Warn("max tool iterations reached", "max", a.maxIterations)
	a.saveTurnMemory(log, msg.SessionKey, turnN, text, "", turnActions, a.maxIterations, true)
	if ch != nil {
		_ = ch.Send(ctx, channel.OutboundMessage{
			ChatID: msg.ChatID,
			Text:   fmt.Sprintf("已达到最大工具调用次数（%d），最终状态可能不完整。", a.maxIterations),
		})
	}
}

func normalizeToolResultContent(res ipc.ToolResult) string {
	msg := normalizedToolMessage{
		Type: "tool_result",
		Tool: res.Name,
		OK:   !res.IsError,
	}

	var payload interface{}
	if err := json.Unmarshal([]byte(res.Output), &payload); err == nil {
		msg.IsJSON = true
		msg.Payload = payload
	} else {
		msg.IsJSON = false
		if res.IsError {
			msg.Error = res.Output
		} else {
			msg.Text = res.Output
		}
	}

	if res.IsError && msg.Error == "" {
		msg.Error = res.Output
	}

	b, err := json.Marshal(msg)
	if err != nil {
		if res.IsError {
			return fmt.Sprintf("[error] %s", res.Output)
		}
		return res.Output
	}
	return string(b)
}

func toIPCUsageEvent(ev provider.UsageEvent) *ipc.LLMUsageEvent {
	return &ipc.LLMUsageEvent{
		At:               ev.At,
		Hint:             ev.Hint,
		Source:           ev.Source,
		ModelKey:         ev.ModelKey,
		Model:            ev.Model,
		InputTokensEst:   ev.InputTokensEst,
		ContextTokensEst: ev.ContextTokensEst,
		PromptTokens:     ev.PromptTokens,
		CompletionTokens: ev.CompletionTokens,
		TotalTokens:      ev.TotalTokens,
		LatencyMs:        ev.LatencyMs,
		StopReason:       ev.StopReason,
		IsError:          ev.IsError,
	}
}

// InjectMessage injects a message into a session and returns the LLM reply (no tool calls).
func (a *Agent) InjectMessage(ctx context.Context, sessionKey, text string) (string, error) {
	sess := a.sessions.Get(sessionKey)
	sess.Append(provider.Message{Role: "user", Content: text}, a.sessions.MaxTurns())
	injectCtx := provider.WithHintSource(ctx, "agent/inject")
	reply, err := a.provider.Complete(injectCtx, sess.HistoryForLLM(provider.HintFromContext(injectCtx)))
	if err != nil {
		return "", err
	}
	sess.Append(provider.Message{Role: "assistant", Content: reply}, a.sessions.MaxTurns())
	return reply, nil
}

// History returns the conversation history for a session key.
func (a *Agent) History(sessionKey string) []provider.Message {
	return a.sessions.Get(sessionKey).History()
}

// ResetSession clears a session's history.
func (a *Agent) ResetSession(sessionKey string) {
	a.sessions.Delete(sessionKey)
}

// SessionKeys returns all active session keys.
func (a *Agent) SessionKeys() []string {
	return a.sessions.Keys()
}

// saveTurnMemory builds a compact TurnSummary and appends it to the session's
// daily JSONL file.  It is called after every completed (or failed) turn.
// Errors are logged as warnings and never propagate to callers.
func (a *Agent) saveTurnMemory(
	log *slog.Logger,
	sessionKey string,
	turnN int,
	userText, replyText string,
	actions []memory.Action,
	iters int,
	isError bool,
) {
	if a.memory == nil {
		return
	}
	summary := memory.BuildSummary(turnN, userText, replyText, actions, iters, isError)
	if err := a.memory.ForSession(sessionKey).SaveTurn(summary); err != nil {
		log.Warn("memory save failed", "err", err)
	}
}

// autoInjectExperiences checks whether any saved experience library is
// relevant to the user's current message and injects it (once per session)
// as additional system context before the first LLM call.
func (a *Agent) autoInjectExperiences(ctx context.Context, msg channel.InboundMessage, ch channel.Channel) {
	if a.expStore == nil {
		return
	}
	metas, err := a.expStore.List()
	if err != nil || len(metas) == 0 {
		return
	}
	msgLower := strings.ToLower(msg.Text)
	for _, m := range metas {
		injectKey := msg.SessionKey + ":" + m.Topic
		if _, already := a.autoInjected.Load(injectKey); already {
			continue
		}
		if !topicMatchesText(m.Topic, msgLower) {
			continue
		}
		content, err := a.expStore.Load(m.Topic)
		if err != nil || content == "" {
			continue
		}
		a.sessions.InjectContext(msg.SessionKey, content)
		a.autoInjected.Store(injectKey, true)
		a.log.Info("auto-injected experience", "topic", m.Topic, "session", msg.SessionKey)
		if ch != nil {
			_ = ch.Send(ctx, channel.OutboundMessage{
				ChatID: msg.ChatID,
				Text:   fmt.Sprintf("💡 已自动加载「%s」经验库", m.Topic),
			})
		}
	}
}

// topicMatchesText reports whether the topic is relevant to the user's message.
// It uses the raw topic tokens (no synonym expansion) and requires ALL of them
// to appear in the already-lowercased text (AND semantics).  This avoids
// false positives caused by overly broad synonym tables – e.g. the synonyms
// of "开发" include "代码" and "项目" which appear in nearly every dev message.
func topicMatchesText(topic, lowerText string) bool {
	tokens := knowledge.TopicTokens(topic)
	if len(tokens) == 0 {
		return false
	}
	for _, kw := range tokens {
		if !strings.Contains(lowerText, kw) {
			return false
		}
	}
	return true
}
