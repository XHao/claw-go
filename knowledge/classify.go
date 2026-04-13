package knowledge

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/XHao/claw-go/provider"
)

const classifySystemPrompt = `你是一个任务分类助手。分析用户消息，输出描述当前任务类型的标签列表。

规则：
- 输出 JSON：{"tags":["tag1","tag2"]}
- 标签应简短（1-2个词），如 "debug"、"deploy"、"golang"、"docker"、"review"、"refactor"
- 最多输出 4 个标签
- 如果无法判断任务类型，输出：{"tags":[]}
- 不要输出任何其他文字`

// TaskClassifier uses a cheap LLM to classify the current task type from
// a user message, returning a list of tags for procedure matching.
type TaskClassifier struct {
	llm provider.Provider
}

// NewTaskClassifier creates a TaskClassifier backed by the given provider.
func NewTaskClassifier(llm provider.Provider) *TaskClassifier {
	return &TaskClassifier{llm: llm}
}

// classifyResult is the JSON structure returned by the LLM.
type classifyResult struct {
	Tags []string `json:"tags"`
}

// Classify returns task type tags for the given user message.
// Returns nil (not an error) when the message is empty or classification fails.
func (c *TaskClassifier) Classify(ctx context.Context, userMsg string) ([]string, error) {
	if strings.TrimSpace(userMsg) == "" {
		return nil, nil
	}
	classCtx := provider.WithModelHint(ctx, provider.ModelHintSummary)
	classCtx = provider.WithHintSource(classCtx, provider.HintSourceClassifyTask)
	msgs := []provider.Message{
		{Role: "system", Content: classifySystemPrompt},
		{Role: "user", Content: userMsg},
	}
	raw, err := c.llm.Complete(classCtx, msgs)
	if err != nil {
		// Classification failure is non-fatal: return empty tags.
		return nil, nil
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var result classifyResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, nil
	}
	return result.Tags, nil
}
