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

// WorkerTaskDef describes a single sub-task for parallel execution.
// This mirrors agent.WorkerTask but lives in the tool package to avoid circular imports.
type WorkerTaskDef struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	ToolsHint   []string `json:"tools_hint,omitempty"`
}

// WorkerResultDef holds the output of a single Worker execution.
// This mirrors agent.WorkerResult but lives in the tool package to avoid circular imports.
type WorkerResultDef struct {
	TaskID string
	Output string
	Error  error
}

// WorkerBatchRunner is a function that executes a batch of worker tasks concurrently.
// It is implemented by agent.RunWorkerBatch (via an adapter in main.go) to avoid
// the tool→agent circular import.
type WorkerBatchRunner func(ctx context.Context, p provider.Provider, tasks []WorkerTaskDef, runner *LocalRunner, sp *string) []WorkerResultDef

// RegisterPlanTasks registers the plan_tasks tool that triggers parallel Worker Agent execution.
// p is the shared LLM provider used by worker agents.
// runWorkers is the batch-execution function (typically an adapter around agent.RunWorkerBatch).
func RegisterPlanTasks(runner *LocalRunner, p provider.Provider, systemPrompt *string, runWorkers WorkerBatchRunner) {
	def := provider.ToolDef{
		Name:        "plan_tasks",
		Description: "Decompose a complex request into independent sub-tasks and execute them concurrently using isolated Worker Agents. Each worker runs its own reasoning loop. Use when tasks are independent and can be parallelized.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"tasks":{"type":"array","description":"List of independent sub-tasks to execute in parallel.","items":{"type":"object","properties":{"id":{"type":"string","description":"Unique task identifier."},"description":{"type":"string","description":"Full task description for the worker agent."},"tools_hint":{"type":"array","items":{"type":"string"},"description":"Optional list of tool names the worker may need."}},"required":["id","description"]}}},"required":["tasks"]}`),
	}

	runner.RegisterGroup("core", def, func(ctx context.Context, argsJSON string, rctx RunContext, progress func(string)) (string, error) {
		var args struct {
			Tasks []WorkerTaskDef `json:"tasks"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return "", fmt.Errorf("plan_tasks: invalid args: %w", err)
		}
		if len(args.Tasks) == 0 {
			return "", fmt.Errorf("plan_tasks: no tasks provided")
		}

		progress(fmt.Sprintf("开始并发执行 %d 个子任务...", len(args.Tasks)))
		results := runWorkers(ctx, p, args.Tasks, runner, systemPrompt)

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("已完成 %d 个子任务：\n\n", len(results)))
		for _, r := range results {
			sb.WriteString(fmt.Sprintf("### 任务 %s\n", r.TaskID))
			if r.Error != nil {
				sb.WriteString(fmt.Sprintf("错误：%v\n\n", r.Error))
			} else {
				sb.WriteString(r.Output + "\n\n")
			}
		}
		return sb.String(), nil
	})
}

// RegisterDaemonTools registers all daemon-internal tools (knowledge, memory,
// session operations) with runner. These tools capture daemon state via
// closures and use rctx.SessionKey at call time.
func RegisterDaemonTools(
	runner *LocalRunner,
	llm provider.Provider,
	mem *memory.Manager,
	getExpStore func() *knowledge.ExperienceStore,
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
		d := knowledge.NewDistiller(llm, mem, getExpStore())
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
		metas, err := getExpStore().List()
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
		content, err := getExpStore().Load(topic)
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
		content, err := getExpStore().Load(topic)
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
