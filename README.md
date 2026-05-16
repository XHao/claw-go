# claw-go

本地命令行 AI 助手，采用**守护进程 + CLI 客户端**架构，支持持久会话、多通道接入、工具调用、知识提炼和多模型分层路由。

---

## 架构概览

```
┌─────────────────────────────────────────────────────┐
│                      Channels                       │
│   CLI (Unix socket)  │  DingTalk  │  WeChat iLink   │
└──────────────┬──────────────────────────────────────┘
               │ IPC (JSON over Unix socket)
┌──────────────▼──────────────────────────────────────┐
│                    Agent (Daemon)                   │
│  ┌───────────┐  ┌──────────┐  ┌──────────────────┐ │
│  │ Sessions  │  │  Memory  │  │    Knowledge     │ │
│  │ (history) │  │ (JSONL)  │  │ distill/procedure│ │
│  └───────────┘  └──────────┘  └──────────────────┘ │
│  ┌──────────────────────────────────────────────┐   │
│  │              Provider (Router)               │   │
│  │  routing → task → summary → thinking (tiers) │   │
│  │  OpenAI / Anthropic / any OpenAI-compatible  │   │
│  └──────────────────────────────────────────────┘   │
│  ┌──────────────────────────────────────────────┐   │
│  │                 Tool Runner                  │   │
│  │  bash / inspect_file / read_file /           │   │
│  │  search_file / write_file / list_files /     │   │
│  │  fetch_url / web_search / memory tools       │   │
│  └──────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────┘
```

守护进程通过 Unix socket 与所有客户端通信，CLI 断开后会话继续保留。

---

## 快速开始

### 安装

```bash
git clone <repo> && cd claw-go
go mod tidy

# 编译并安装到 $GOPATH/bin（更新本地 claw 命令）
make deploy

# 或仅在项目目录下生成二进制
make build
```

### 初始化

```bash
# 初始化 ~/.claw/ 目录、写入配置模板、注册开机自启动服务
claw install

# 编辑配置，填入 API Key
$EDITOR ~/.claw/config.yaml
```

最小配置：

```yaml
models:
  default_task:
    base_url: "https://api.openai.com/v1"
    api_key: "${OPENAI_API_KEY}"   # 或 export OPENAI_API_KEY=sk-...
    model: "gpt-4o-mini"
    max_tokens: 4096

primary_model: "default_task"
```

### 启动与使用

```bash
claw serve          # 启动守护进程（前台）
# 新开终端：
claw                # 连接守护进程，进入交互界面
```

---

## 命令参考

| 命令 | 说明 |
|---|---|
| `claw` | 连接守护进程，进入交互 CLI |
| `claw serve` | 在前台启动守护进程 |
| `claw install` | 初始化目录 + 注册开机自启动服务 |
| `claw uninstall` | 移除自启动服务 |
| `claw restart` | 重启守护进程服务 |
| `claw reload` | 热重载 prompt 文件（无需重启） |

**make 快捷目标：**

| 命令 | 说明 |
|---|---|
| `make build` | 编译到 `./claw` |
| `make deploy` | `go install` — 更新 `$GOPATH/bin/claw` |
| `make serve` | 编译并在前台启动守护进程 |
| `make install` | 编译并运行 `claw install` |
| `make test` | 运行全部测试 |
| `make clean` | 删除本地编译产物 |

---

## CLI 交互参考

| 输入 | 说明 |
|---|---|
| `/help` | 显示帮助 |
| `/reset` | 清空当前会话历史 |
| `/ml` | 多行输入模式（`/send` 发送，`/abort` 取消） |
| `\ + Enter` | 行续写 |
| `!<cmd>` | 执行本地 shell 命令并显示输出 |
| `/learn "<主题>"` | 从历史记忆中提炼经验库（Map-Reduce） |
| `/exp ls` | 列出经验库 |
| `/exp show <名称>` | 查看经验内容 |
| `/exp use <名称>` | 手动注入经验到当前会话 |
| `/exp rm <名称>` | 删除经验 |
| `Ctrl+C` | 思考中取消当前任务；空输入时退出 |

---

## 核心功能

### 持久会话
会话历史写入磁盘（`~/.claw/sessions/`），CLI 断开、daemon 重启后仍可恢复。支持多会话，CLI 启动时展示会话选择界面。

### 多模型分层路由
通过 `routing_policy` 将模型分为四个层级，按任务复杂度自动调度：

| 层级 | 用途 |
|---|---|
| `routing_model` | 超轻量分类器，判断任务类型（约 200 ms） |
| `task_model` | 通用对话模型（默认） |
| `summary_model` | 长上下文摘要，用于历史压缩和知识提炼 |
| `thinking_model` | 深度推理模型（复杂编码、架构设计等） |

**自动路由**：无需手动切换，系统先用本地关键词启发式规则（如"架构"、"重构"、"root cause"等），再通过路由模型分类，自动决定使用 task 还是 thinking 层级。

### 工具调用
支持最多 20 次迭代（可配置）的工具调用循环。只读工具自动并行执行。内置工具：

| 工具 | 说明 |
|---|---|
| `bash` | 执行 shell 命令（支持超时和命令白名单） |
| `inspect_file` | 检查文件类型、大小、编码，给出推荐分析策略 |
| `read_file` | 读取文件（支持偏移量/分段读取） |
| `search_file` | 文件内搜索（line 模式 / byte 模式，支持正则） |
| `write_file` | 写入文件 |
| `list_files` | 列出目录内容 |
| `fetch_url` | 抓取并分析 URL 内容 |
| `web_search` | Tavily 网络搜索（需配置 API Key） |
| `recall_memory` | 跨会话语义检索历史记忆 |

### 知识系统
- **经验提炼**：`/learn "<主题>"` 触发 Map-Reduce 管道，从全部会话历史中提炼结构化 Markdown 经验文件，自动注入后续对话。
- **流程注入（Procedure）**：在 `~/.claw/procedures/` 下放置带 `tags` 元数据的 Markdown 文件，任务分类器自动匹配并注入相关流程到上下文。
- **短期记忆**：每轮对话结束后自动摘要并持久化，支持基于关键词或嵌入向量的跨会话语义检索。

### Prompt 注入防护
自动扫描工具输出中的常见注入模式（绕过指令、凭据提取、不可见 Unicode 字符等），检测到威胁时替换为安全占位符并记录到日志。

### 多 Agent（Worker）
支持并发子任务执行（最多 3 个 Worker 并发），每个 Worker 持有独立会话和工具环境。

---

## 消息通道

### CLI（默认）
通过 Unix socket 连接守护进程，支持流式输出、Markdown 渲染、多会话切换。

### DingTalk（钉钉）
使用 Stream API（WebSocket 长连接），无需公网 HTTP 服务器。

```yaml
dingtalk:
  enabled: true
  client_id: "dingxxxxxxxx"    # AppKey
  client_secret: "xxxxxxxx"    # AppSecret
```

### WeChat iLink
使用 HTTP 长轮询，首次启动时在终端打印二维码，扫码后 token 自动保存复用。

```yaml
weixin:
  enabled: true
  token_file: "~/.claw/weixin-token.json"   # 可选，默认路径
```

---

## 配置详解

完整配置模板见 [cmd/claw/config.example.yaml](cmd/claw/config.example.yaml)。

### 上下文管理

| 配置项 | 说明 |
|---|---|
| `max_history_turns` | 保留的最大对话轮数 |
| `max_history_tokens` | 历史总 token 上限（超出时压缩摘要） |
| `recent_raw_turns` | 最近 N 轮保留完整内容，其余用摘要替代 |
| `history_chars_per_token` | 字符/token 比例估算（默认 3.0） |

### 分层历史预算

```yaml
history_budget_scale:
  router:   0.2    # 路由调用仅需少量上下文
  task:     1.0    # 正常对话用全量历史
  summary:  2.0    # 摘要任务可用更多
  thinking: 1.5    # 深度推理任务
```

### 工具配置

```yaml
tools:
  enabled: true
  max_iterations: 20
  bash_timeout_seconds: 30
  bash_allowed_commands: [git, go, npm]   # 为空则不限制
  allowed: [bash, read_file, search_file] # 为空则全部启用
```

### 系统 Prompt 目录
`~/.claw/prompts/` 下的 `.md` 文件会按文件名排序拼接为系统 prompt，支持热重载（`claw reload`）。优先级高于 config 中的 `system_prompt` 字段。

### Anthropic Claude 扩展思考

```yaml
models:
  claude_think:
    type: anthropic
    api_key: "${ANTHROPIC_API_KEY}"
    model: "claude-opus-4-5"
    max_tokens: 16000
    thinking_budget: 10000   # 启用扩展思考，必须 < max_tokens
routing_policy:
  thinking_model: "claude_think"
```

---

## 数据目录

| 路径 | 说明 |
|---|---|
| `~/.claw/config.yaml` | 主配置文件 |
| `~/.claw/sessions/` | 持久化会话历史 |
| `~/.claw/data/memory/` | 短期记忆（JSONL） |
| `~/.claw/data/experiences/` | 经验库（Markdown） |
| `~/.claw/procedures/` | 流程注入文件（Markdown + YAML frontmatter） |
| `~/.claw/prompts/` | 系统 prompt 分层文件 |
| `~/.claw/logs/` | 守护进程日志 |

**自启动服务位置：**
- macOS：`~/Library/LaunchAgents/com.xhao.claw-go.daemon.plist`
- Linux：`~/.config/systemd/user/claw-go.service`

---

## 开发

```bash
make test           # 运行全部测试
go test ./agent/... # 测试单个包
make build          # 编译到 ./claw
make deploy         # go install — 更新 $GOPATH/bin/claw
```
