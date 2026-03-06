package skill

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

// BuildRegistry creates and returns a Registry populated with all built-in
// skills. Each handler is a closure over the provided daemon-side deps.
func BuildRegistry(
	llm provider.Provider,
	mem *memory.Manager,
	expStore *knowledge.ExperienceStore,
	sessions *session.Store,
) *Registry {
	reg := NewRegistry()

	// distill_knowledge ---------------------------------------------------
	reg.Register(&Def{
		ToolDef: provider.ToolDef{
			Name: "distill_knowledge",
			Description: "Scan all conversation history and distil knowledge about a " +
				"named topic into a persistent Markdown experience library " +
				"(~/.claw/data/experiences/). Call when user wants to: save/organise/" +
				"extract/summarise learnings, document knowledge, " +
				"or says: \u6574\u7406\u7ecf\u9a8c/\u63d0\u70bc\u77e5\u8bc6/\u603b\u7ed3\u5185\u5bb9/\u628a X \u76f8\u5173\u7684\u5bf9\u8bdd\u6574\u7406\u6210\u6587\u6863. " +
				"topic must be a short descriptive label, e.g. \"Docker\" or \"Go\u5e76\u53d1\".",
			Parameters: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"Short label for the subject, e.g. \"Docker\" or \"Go\u5e76\u53d1\""}},"required":["topic"]}`),
		},
		Handler: func(ctx context.Context, args map[string]string, sessionKey string, progress func(string)) (string, error) {
			topic := strings.TrimSpace(args["topic"])
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
		},
	})

	// list_experiences ----------------------------------------------------
	reg.Register(&Def{
		ToolDef: provider.ToolDef{
			Name: "list_experiences",
			Description: "Return a list of all saved experience libraries with sizes and dates. " +
				"Call when user asks: what knowledge is saved, \u6709\u54ea\u4e9b\u7ecf\u9a8c\u5e93, \u6211\u4fdd\u5b58\u4e86\u4ec0\u4e48\u77e5\u8bc6, show my knowledge base.",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Handler: func(ctx context.Context, args map[string]string, sessionKey string, progress func(string)) (string, error) {
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
		},
	})

	// show_experience -----------------------------------------------------
	reg.Register(&Def{
		ToolDef: provider.ToolDef{
			Name: "show_experience",
			Description: "Return the full Markdown content of a saved experience library. " +
				"Call when the user wants to read/view/\u67e5\u770b a specific experience document.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"The topic name"}},"required":["topic"]}`),
		},
		Handler: func(ctx context.Context, args map[string]string, sessionKey string, progress func(string)) (string, error) {
			topic := strings.TrimSpace(args["topic"])
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
		},
	})

	// inject_experience ---------------------------------------------------
	reg.Register(&Def{
		ToolDef: provider.ToolDef{
			Name: "inject_experience",
			Description: "Load a saved experience library and inject it as system context " +
				"into the current conversation, immediately enhancing future answers. " +
				"Call when user says: use/load/apply my X knowledge into this chat, " +
				"\u628a X \u7684\u7ecf\u9a8c\u52a0\u5230\u5bf9\u8bdd, \u7528\u6211\u4e4b\u524d\u603b\u7ed3\u7684 X \u77e5\u8bc6.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"Topic of the experience to inject"}},"required":["topic"]}`),
		},
		Handler: func(ctx context.Context, args map[string]string, sessionKey string, progress func(string)) (string, error) {
			topic := strings.TrimSpace(args["topic"])
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
			sessions.InjectContext(sessionKey, content)
			return fmt.Sprintf(
				"已将主题 %q 的经验库注入当前对话，后续回答将参考其中内容。",
				topic,
			), nil
		},
	})

	// delete_experience ---------------------------------------------------
	reg.Register(&Def{
		ToolDef: provider.ToolDef{
			Name: "delete_experience",
			Description: "Permanently delete a saved experience library. " +
				"Only call when user explicitly asks to delete/remove/\u6e05\u9664/\u5220\u6389 a knowledge library.",
			Parameters: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"The topic to delete"}},"required":["topic"]}`),
		},
		Handler: func(ctx context.Context, args map[string]string, sessionKey string, progress func(string)) (string, error) {
			topic := strings.TrimSpace(args["topic"])
			if topic == "" {
				return "", fmt.Errorf("delete_experience: topic is required")
			}
			if err := expStore.Delete(topic); err != nil {
				return "", fmt.Errorf("delete_experience: %w", err)
			}
			return fmt.Sprintf("已删除主题 %q 的经验库。", topic), nil
		},
	})

	// reset_conversation --------------------------------------------------
	reg.Register(&Def{
		ToolDef: provider.ToolDef{
			Name: "reset_conversation",
			Description: "Clear current conversation history and start fresh. " +
				"Call when user says: clear/reset/wipe chat, start over, forget everything, " +
				"\u6e05\u7a7a\u5bf9\u8bdd, \u91cd\u65b0\u5f00\u59cb, \u5fd8\u6389\u4e4b\u524d\u7684\u5185\u5bb9.",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Handler: func(ctx context.Context, args map[string]string, sessionKey string, progress func(string)) (string, error) {
			if sessionKey == session.MainSessionKey {
				sessions.ClearHistory(sessionKey)
				if mem != nil {
					_ = mem.ClearSession(sessionKey)
				}
				return "对话历史已清空，可以重新开始。", nil
			}
			sessions.Delete(sessionKey)
			if mem != nil {
				_ = mem.DeleteSession(sessionKey)
			}
			return "对话已清空，开始新对话！", nil
		},
	})

	return reg
}
