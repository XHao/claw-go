package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/XHao/claw-go/knowledge"
	"github.com/XHao/claw-go/memory"
	"github.com/XHao/claw-go/provider"
	"github.com/XHao/claw-go/session"
)

// RegisterDaemonTools registers all daemon-internal tools (knowledge, memory,
// session operations) with runner. These tools capture daemon state via
// closures and use rctx.SessionKey at call time.
func RegisterDaemonTools(
	runner *LocalRunner,
	llm provider.Provider,
	mem *memory.Manager,
	expStore *knowledge.ExperienceStore,
	sessions *session.Store,
) {
	// distill_knowledge -------------------------------------------------------
	runner.RegisterGroup("knowledge", provider.ToolDef{
		Name:        "distill_knowledge",
		Description: "Scan conversation history and distil knowledge about a topic into a persistent Markdown experience library. topic is a short descriptive label, e.g. \"Docker\" or \"Go concurrency\".",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"Short label for the subject, e.g. \"Docker\" or \"Go concurrency\""}},"required":["topic"]}`),
	}, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		var p struct {
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return "", fmt.Errorf("distill_knowledge: invalid args: %w", err)
		}
		topic := strings.TrimSpace(p.Topic)
		if topic == "" {
			return "", fmt.Errorf("distill_knowledge: topic is required")
		}
		d := knowledge.NewDistiller(llm, mem, expStore)
		content, err := d.Distill(ctx, topic, progress)
		if err != nil {
			return "", fmt.Errorf("distill_knowledge: %w", err)
		}
		lines := strings.Count(content, "\n") + 1
		return fmt.Sprintf(
			"主题 %q 的经验库已保存（共 %d 行）。"+
				"可告知用户使用 `/exp use %q` 命令加载，或调用 inject_experience 直接注入当前对话。",
			topic, lines, topic,
		), nil
	})

	// list_experiences --------------------------------------------------------
	runner.RegisterGroup("knowledge", provider.ToolDef{
		Name:        "list_experiences",
		Description: "List all saved experience libraries with their sizes and last-update dates.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
	}, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		metas, err := expStore.List()
		if err != nil {
			return "", fmt.Errorf("list_experiences: %w", err)
		}
		if len(metas) == 0 {
			return "暂无已保存的经验库，请先通过 distill_knowledge 创建一份。", nil
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "已保存 %d 份经验库：\n\n", len(metas))
		for i, m := range metas {
			fmt.Fprintf(&sb, "%d. %-28s  %.1f KB  （更新于 %s）\n",
				i+1, m.Topic, float64(m.Size)/1024, m.UpdatedAt.Format("2006-01-02"))
		}
		return sb.String(), nil
	})

	// show_experience ---------------------------------------------------------
	runner.RegisterGroup("knowledge", provider.ToolDef{
		Name:        "show_experience",
		Description: "Return the full Markdown content of a saved experience library by topic name.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"The topic name"}},"required":["topic"]}`),
	}, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		var p struct {
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return "", fmt.Errorf("show_experience: invalid args: %w", err)
		}
		topic := strings.TrimSpace(p.Topic)
		if topic == "" {
			return "", fmt.Errorf("show_experience: topic is required")
		}
		content, err := expStore.Load(topic)
		if err != nil {
			return "", fmt.Errorf("show_experience: %w", err)
		}
		if content == "" {
			return fmt.Sprintf("未找到主题 %q 的经验库，请先使用 distill_knowledge 进行提炼。", topic), nil
		}
		return content, nil
	})

	// inject_experience -------------------------------------------------------
	runner.RegisterGroup("knowledge", provider.ToolDef{
		Name:        "inject_experience",
		Description: "Load a saved experience library by topic and inject it as system context into the current conversation.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"Topic of the experience to inject"}},"required":["topic"]}`),
	}, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		var p struct {
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &p); err != nil {
			return "", fmt.Errorf("inject_experience: invalid args: %w", err)
		}
		topic := strings.TrimSpace(p.Topic)
		if topic == "" {
			return "", fmt.Errorf("inject_experience: topic is required")
		}
		content, err := expStore.Load(topic)
		if err != nil {
			return "", fmt.Errorf("inject_experience load: %w", err)
		}
		if content == "" {
			return fmt.Sprintf("未找到主题 %q 的经验库，请先运行 distill_knowledge 进行提炼。", topic), nil
		}
		sessions.InjectContext(rctx.SessionKey, content)
		return fmt.Sprintf(
			"已将主题 %q 的经验库注入当前对话，后续回答将参考其中内容。",
			topic,
		), nil
	})
}
