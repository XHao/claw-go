package knowledge

import (
	"context"
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
