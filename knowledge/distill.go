package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
)

const (
	maxCandidates = 80 // max turns to feed into the Map phase
	mapBatchSize  = 10 // turns per Map LLM call
)

// Distiller implements a Map-Reduce knowledge distillation pipeline.
//
// Map phase:  feed batches of relevant turns to the LLM and extract bullet
// points pertaining to topic.
//
// Reduce phase: merge all Map outputs with any existing experience file into
// a clean, de-duplicated Markdown document.
type Distiller struct {
	llm   provider.Provider
	mem   *memory.Manager
	store *ExperienceStore
}

// NewDistiller creates a Distiller that uses llm for LLM calls, mem to scan
// conversation memory, and store to persist experience files.
func NewDistiller(llm provider.Provider, mem *memory.Manager, store *ExperienceStore) *Distiller {
	return &Distiller{llm: llm, mem: mem, store: store}
}

// WithStore returns a shallow copy of the Distiller pointing at a different
// ExperienceStore. Used to redirect distillation to the current Agent's directory.
func (d *Distiller) WithStore(store *ExperienceStore) *Distiller {
	return &Distiller{llm: d.llm, mem: d.mem, store: store}
}

// ProgressFunc is called with short status messages during Distill.
type ProgressFunc func(msg string)

// Distill runs the full pipeline for topic and returns the new Markdown content.
// progress is called with human-readable status messages throughout.
func (d *Distiller) Distill(ctx context.Context, topic string, progress ProgressFunc) (string, error) {
	if progress == nil {
		progress = func(string) {}
	}

	// 1. Load all turns from all sessions.
	progress("加载历史记忆…")
	turns, err := d.loadAllTurns()
	if err != nil {
		return "", fmt.Errorf("distill: load turns: %w", err)
	}
	if len(turns) == 0 {
		return "", fmt.Errorf("没有可用的历史记忆，请先与 AI 对话再提炼经验")
	}

	// 2. Local keyword filtering — no LLM cost.
	progress(fmt.Sprintf("关键词过滤 %d 条记忆…", len(turns)))
	keywords := ExtractKeywords(topic)
	relevant := FilterRelevant(turns, keywords, maxCandidates)
	if len(relevant) == 0 {
		return "", fmt.Errorf("历史记忆中未找到与 %q 相关的内容，请先进行相关对话", topic)
	}

	// 3. Map phase — process in batches.
	// Use ModelHintSummary so RouterProvider selects the cheap long-context
	// model (e.g. Gemini Flash) for these repeated extraction calls.
	mapCtx := provider.WithModelHint(ctx, provider.ModelHintSummary)
	chunks := chunkTurns(relevant, mapBatchSize)
	var mapResults []string
	for i, chunk := range chunks {
		progress(fmt.Sprintf("Map %d/%d — 提炼知识片段…", i+1, len(chunks)))
		// Tag each batch with its index so metrics show exactly which batch was processed.
		batchCtx := provider.WithHintSource(mapCtx, provider.HintSourceDistillMap(i+1, len(chunks)))
		result, err := d.mapBatch(batchCtx, topic, chunk)
		if err != nil {
			return "", fmt.Errorf("distill: map batch %d: %w", i, err)
		}
		trimmed := strings.TrimSpace(result)
		if trimmed != "NONE" && trimmed != "" {
			mapResults = append(mapResults, result)
		}
	}
	if len(mapResults) == 0 {
		return "", fmt.Errorf("提炼结果为空：历史对话中没有足够的与 %q 相关的信息", topic)
	}

	// 4. Reduce phase — merge map outputs with existing experience.
	// Use ModelHintSummary consistent with the map phase so both legs are
	// routed to the cheap long-context tier (see doc: map/reduce 分别打 source).
	progress("Reduce — 整合并去重…")
	existing, _ := d.store.Load(topic)
	reduceCtx := provider.WithModelHint(ctx, provider.ModelHintSummary)
	reduceCtx = provider.WithHintSource(reduceCtx, provider.HintSourceDistillReduce)
	final, err := d.reduce(reduceCtx, topic, mapResults, existing)
	if err != nil {
		return "", fmt.Errorf("distill: reduce: %w", err)
	}

	// 5. Prepend header and save.
	header := fmt.Sprintf("# %s\n\n> 最后更新: %s\n\n", topic, time.Now().Format("2006-01-02 15:04"))
	content := header + final
	progress("保存经验库…")
	if err := d.store.Save(topic, content); err != nil {
		return "", fmt.Errorf("distill: save: %w", err)
	}
	return content, nil
}

// loadAllTurns scans every session in the memory manager and returns all turns.
func (d *Distiller) loadAllTurns() ([]memory.TurnSummary, error) {
	sessions, err := d.mem.AllSessions()
	if err != nil {
		return nil, err
	}
	var all []memory.TurnSummary
	for _, key := range sessions {
		st := d.mem.ForSession(key)
		turns, err := st.LoadRecent(0) // 0 = all turns
		if err != nil {
			continue
		}
		all = append(all, turns...)
	}
	return all, nil
}

// chunkTurns splits turns into chunks of at most size elements.
func chunkTurns(turns []memory.TurnSummary, size int) [][]memory.TurnSummary {
	var out [][]memory.TurnSummary
	for i := 0; i < len(turns); i += size {
		end := i + size
		if end > len(turns) {
			end = len(turns)
		}
		out = append(out, turns[i:end])
	}
	return out
}

const mapSystemPrompt = `你是一个知识提炼助手。从提供的对话片段中，仅提取与指定主题直接相关的可复用经验要点。

规则：
- 仅输出与主题相关的 Markdown 无序列表（- 开头）。
- 每条要点言简意赅，去除个人隐私、一次性信息、无关内容。
- 如果该批次完全不包含与主题相关的信息，只输出一个词：NONE
- 不要输出其他任何文字、标题或解释。`

// mapBatch calls the LLM to extract knowledge bullets from one batch of turns.
func (d *Distiller) mapBatch(ctx context.Context, topic string, turns []memory.TurnSummary) (string, error) {
	batch := FormatBatchForLLM(turns)
	userMsg := fmt.Sprintf("主题: %s\n\n对话片段:\n%s", topic, batch)
	msgs := []provider.Message{
		{Role: "system", Content: mapSystemPrompt},
		{Role: "user", Content: userMsg},
	}
	return d.llm.Complete(ctx, msgs)
}

const reduceSystemPrompt = `你是一个知识整理专家。将提供的知识要点合并为一份结构化的 Markdown 经验文档。

规则：
- 使用 ## 分节，按逻辑关系分组。
- 去除重复内容，保留最具体、最有价值的表述。
- 删除一次性事实（如某次具体的错误堆栈）。
- 如有旧版经验，优先保留新版内容，但不丢失旧版中仍有价值的内容。
- 如果新旧内容存在矛盾，在更新后的条目末尾加注 [已更新] 并在括号内说明变化原因，例如：
  "使用 overlay 模式（[已更新]：比 bridge 模式更适合多主机场景）"
- 如果旧版某条经验已不再适用，保留原文但在末尾加注 [已废弃] 并说明原因，不要直接删除。
- 只输出 Markdown 正文，不要输出 # 一级标题（调用者统一添加）。`

// reduce calls the LLM to merge all map results (and possibly an existing file)
// into the final Markdown body (without the top-level # headline).
func (d *Distiller) reduce(ctx context.Context, topic string, mapResults []string, existing string) (string, error) {
	var sb strings.Builder
	sb.WriteString("主题: ")
	sb.WriteString(topic)
	sb.WriteString("\n\n## 新提炼的要点\n\n")
	for _, r := range mapResults {
		sb.WriteString(r)
		sb.WriteString("\n")
	}
	if existing != "" {
		sb.WriteString("\n## 现有经验（请在整合时参考）\n\n")
		sb.WriteString(existing)
		sb.WriteString("\n")
	}
	msgs := []provider.Message{
		{Role: "system", Content: reduceSystemPrompt},
		{Role: "user", Content: sb.String()},
	}
	return d.llm.Complete(ctx, msgs)
}

const evalSystemPrompt = `你是一个知识价值评估助手。判断给定的对话摘要是否包含值得长期记忆的新知识或经验。

规则：
- 如果包含可复用的技术知识、解决方案或用户偏好，输出 JSON：{"valuable":true,"topic":"<简短主题>","summary":"<一句话摘要>"}
- 如果是闲聊、简单问答、或无可复用价值，只输出：{"valuable":false}
- topic 应简短（1-3个词），如 "docker"、"golang并发"、"用户偏好"
- 如果本轮内容与已有知识存在明显矛盾（新方案替代了旧方案），在 summary 中以 "更新：" 开头说明，例如：
  "更新：overlay 模式替代 bridge 模式用于多主机 docker 网络"
- 不要输出任何其他文字`

// EvalTurnResult is the structured output from EvalTurn.
type EvalTurnResult struct {
	Valuable bool   `json:"valuable"`
	Topic    string `json:"topic,omitempty"`
	Summary  string `json:"summary,omitempty"`
}

// EvalTurn asks a cheap LLM whether the given TurnSummary contains knowledge
// worth persisting. Returns EvalTurnResult with Valuable=false on any error.
func (d *Distiller) EvalTurn(ctx context.Context, t memory.TurnSummary) EvalTurnResult {
	batch := FormatBatchForLLM([]memory.TurnSummary{t})
	msgs := []provider.Message{
		{Role: "system", Content: evalSystemPrompt},
		{Role: "user", Content: batch},
	}
	evalCtx := provider.WithModelHint(ctx, provider.ModelHintSummary)
	evalCtx = provider.WithHintSource(evalCtx, provider.HintSourceDistillEval)
	raw, err := d.llm.Complete(evalCtx, msgs)
	if err != nil {
		return EvalTurnResult{}
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var result EvalTurnResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return EvalTurnResult{}
	}
	return result
}

// Store returns the ExperienceStore used by this Distiller.
func (d *Distiller) Store() *ExperienceStore {
	return d.store
}

// ReduceSystemPromptForTest returns the reduce system prompt for testing.
func ReduceSystemPromptForTest() string { return reduceSystemPrompt }

// EvalSystemPromptForTest returns the eval system prompt for testing.
func EvalSystemPromptForTest() string { return evalSystemPrompt }
