# claw-go

本地命令行 AI 助手（守护进程 + CLI），支持持久会话、工具调用、短期记忆、知识提炼与本地 shell 执行。

## 快速开始

```bash
git clone <repo> && cd claw-go
go mod tidy
go build -o claw .

# 一次性：初始化 ~/.claw、写入配置模板、注册开机启动
./claw install

# 配置 API Key（任选一种）
$EDITOR ~/.claw/config.yaml
export OPENAI_API_KEY=sk-...

# 启动守护进程
./claw serve

# 新开终端连接 CLI
./claw
```

## 常用命令

| 命令 | 说明 |
|---|---|
| `claw serve` | 启动守护进程 |
| `claw` | 连接守护进程并进入交互界面 |
| `claw install` | 初始化目录并注册启动服务 |
| `claw uninstall` | 移除启动服务 |

## CLI 快速参考

| 输入 | 作用 |
|---|---|
| `/help` | 显示帮助 |
| `/reset` | 清空当前会话历史 |
| `/ml` | 多行输入模式（`/send` 发送，`/abort` 取消） |
| `\ + Enter` | 行续写 |
| `!<cmd>` | 执行本地 shell 命令 |
| `/learn "主题"` | 从记忆提炼经验库 |
| `/exp ls/show/use/rm` | 管理经验库 |
| `Ctrl+C` | 思考中取消当前任务；空输入时退出 |

## 配置（简版）

最小必填项：`models` + `primary_model`。

完整示例见 [config.example.yaml](config.example.yaml)。

```yaml
models:
  default_task:
    type: openai
    base_url: "https://api.openai.com/v1"
    model: "gpt-4o-mini"
    # api_key: "sk-..." 或使用 OPENAI_API_KEY

primary_model: "default_task"
```

常用可调项：

- `max_history_turns`：会话保留轮数上限。
- `tools.enabled`：启用/关闭工具调用。
- `routing_policy`：启用 router/task/summary/thinking 分层。

上下文治理（高级）：

- `max_history_tokens`
- `recent_raw_turns`
- `history_chars_per_token`
- `history_budget_scale.router/task/summary/thinking`

## 当前能力

- 持久会话（断开 CLI 后可继续）
- 工具调用（含 `inspect_file`、`grep_file`、`search_file`、`read_file` 等）
- 机器可读工具结果与稳健中断
- 自动路由（task/thinking）
- 经验提炼与会话注入

## 详细文档

- 架构与路由：docs/LLM/README.md
- 配置模板：config.example.yaml
│   └── startup.go       # 开机自启动：macOS LaunchAgent / Linux systemd
├── tool/
│   └── executor.go      # ShellExecutor：本地 shell 命令执行（客户端侧）
└── config.example.yaml  # 带注释的完整配置示例
```

---

## 运行测试

```bash
go test ./...
```

## 为什么选择 Go？

| 关注点 | TypeScript 原版 | Go 版 |
|---|---|---|
| 启动时间 | ~1–3 s (Bun) | < 50 ms |
| 空闲内存 | ~100–200 MB | ~10–20 MB |
| 并发模型 | 事件循环 | 原生 goroutine（M:N 调度） |
| 部署方式 | 需要 Node/Bun 运行时 | 单静态二进制，无依赖 |
| 优雅退出 | 手动实现 | `signal.NotifyContext` + `Shutdown` |
| 类型安全 | TypeScript（运行时擦除） | 编译时强类型，无 `any` |
