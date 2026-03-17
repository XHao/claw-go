# claw-go

本地运行的命令行 AI 助手，守护进程 + CLI 客户端架构，支持**持久对话历史**、**工具调用**、**短期记忆**、**知识提炼**以及**执行 shell 交互命令**的能力。

> 原型参考自 claw（TypeScript），本项目为 Go 实现的简易版本。

## 功能总览

| 能力 | 说明 |
|---|---|
| 本地部署 | 编译为单一静态二进制，无运行时依赖，启动 < 50ms |
| 守护进程 | `claw serve` 持久运行，保留所有对话历史；CLI 断连后历史不丢失 |
| 对话管理 | 连接时选择已有对话继续，或新建并命名；支持同时保存多个独立对话 |
| 命令行界面 (CLI) | `claw` 连接守护进程，内置 readline REPL，支持历史、彩色输出 |
| Tab 补全 | `/` 命令补全、子命令补全、`!` shell 可执行文件名补全 |
| 单连接限制 | 每个对话同一时间只允许一个 CLI 客户端接入 |
| 工具调用 | 守护进程侧 agentic 循环：bash / read_file / write_file / list_files |
| 短期记忆 | 每轮对话自动写入 JSONL 日志，跨会话聚合分析 |
| 知识提炼 | `/learn` 使用 Map-Reduce 将历史记忆提炼为可复用经验库 |
| 经验管理 | `/exp` 查询、挂载经验到会话上下文，实时增强 AI 回答 |
| 可配置化 | YAML 配置文件 + 环境变量覆盖 |
| 执行交互命令 | `!<cmd>` 前缀在本地运行任意 shell 命令，TUI 程序（vim、htop 等）完整支持 |
| 本地 LLM | 兼容任意 OpenAI 格式端点（Ollama、vLLM、LM Studio 等） |

## 架构

```
┌──────────────────────────────────────────────────────────────┐
│                    claw serve (守护进程)                      │
│                                                              │
│  channel.SocketChannel ──► agent.Dispatch()                  │
│  └ Unix Domain Socket       ├ session.Store   (对话历史)     │
│    (每对话单连接限制)        ├ memory.Manager  (短期记忆)     │
│                             ├ provider.Complete() (LLM)      │
│                             └ channel.Send()  (回复)         │
│                                                              │
│  对话历史保留在内存，CLI 断连/重连均可继续上次会话             │
└──────────────────┬───────────────────────────────────────────┘
                   │  Unix Domain Socket (~/.claw/claw-go.sock)
┌──────────────────▼───────────────────────────────────────────┐
│                   claw (CLI 客户端)                           │
│                                                              │
│  readline REPL + Tab补全 + !<cmd> 本地 shell 执行            │
│  /learn  Map-Reduce 知识提炼                                  │
│  /exp    经验库管理 & 挂载                                    │
└──────────────────────────────────────────────────────────────┘
```

## 快速开始

```bash
git clone <repo> && cd claw-go
go mod tidy
go build -o claw .

# 1. 初始化数据目录 ~/.claw，生成配置模板，注册开机自启动（一次性操作）
./claw install

# 编辑配置，填写 api_key（或通过环境变量传入）
$EDITOR ~/.claw/config.yaml
export OPENAI_API_KEY=sk-...    # 也可直接用环境变量覆盖

# 2. 启动守护进程（install 后开机自动启动，也可手动启动）
./claw serve &

# 3. 连接交互式 CLI
./claw
```

启动后进入对话选择界面：

```
Conversations:
   1. work-project                    12 turns
   2. learning-go                      5 turns
   n. New conversation

Select [1-2 / n]: n
Conversation name: my-project
Conversation: my-project  (type /help for commands)
```

如果某个对话已被另一个终端占用，选择界面会在名称旁显示 `[busy]`，选择它会得到错误提示并刷新列表：

```
Conversations:
   1. work-project                    12 turns  [busy]
   2. learning-go                      5 turns
   n. New conversation

Select [1-2 / n]: 1
Error: conversation "work-project" is already in use by another client
Select [1-2 / n]: 2
Conversation: learning-go  (type /help for commands)

You ❯ 你好
Assistant: 你好！有什么我可以帮你的吗？

You ❯ !ls -la
...（shell 输出）...

You ❯ exit
```

再次连接时，之前的对话仍然保留：

```
Conversations:
   1. work-project                    12 turns
   2. learning-go                      5 turns
   3. my-project                        1 turn
   n. New conversation

Select [1-3 / n]: 3
Conversation: my-project  (type /help for commands)

You ❯    ← 继续上次的对话
```

### 命令行参数

守护进程与 CLI 客户端共用相同的参数：

| 参数 | 默认值 | 说明 |
|---|---|
| `--config` | 自动探测 | 配置文件路径（YAML）；留空时依次检测 `$OPENCLAW_CONFIG_PATH` → `~/.claw/config.yaml` → `./config.yaml` |
| `--log-level` | `info` | 日志级别（仅 `serve` 模式有效）：`debug` / `info` / `warn` / `error` |

**子命令：**

| 命令 | 说明 |
|---|---|
| `claw serve` | 启动守护进程（前台运行，持有会话历史） |
| `claw install` | 初始化 `~/.claw`，生成配置模板，注册开机自启动服务 |
| `claw uninstall` | 移除开机自启动注册 |
| `claw` | 连接守护进程，进入交互式 CLI |

**平台实现：**

| 平台 | 机制 | 服务文件 |
|---|---|---|
| macOS | LaunchAgent (`launchctl`) | `~/Library/LaunchAgents/com.xhao.claw-go.daemon.plist` |
| Linux | systemd user unit | `~/.config/systemd/user/claw.service` |

---

## 配置参考

完整示例见 [config.example.yaml](config.example.yaml)。

### 数据目录

`claw install` 会自动创建以下目录结构（默认 `~/.claw`，可用环境变量 `$OPENCLAW_STATE_DIR` 覆盖）：

```
~/.claw/
├── config.yaml               配置文件（install 时自动生成模板）
├── sessions/                 每个对话一个 JSON 文件，守护进程重启后历史不丢
├── logs/
│   └── claw-go.log          守护进程日志
├── history                   readline 输入历史
└── data/
    ├── memory/               短期记忆（按会话+日期分片的 JSONL）
    │   ├── main/
    │   │   └── 2024-01-15.jsonl
    │   └── my-project/
    │       └── 2024-01-15.jsonl
    └── experiences/          经验库（/learn 生成的 Markdown 文件）
        ├── linux_调试.md
        └── docker.md
```

### 顶层

| 字段 | 默认值 | 说明 |
|---|---|---|
| `socket_path` | `~/.claw/claw-go.sock`（或 `$XDG_RUNTIME_DIR/claw-go.sock`） | 守护进程监听的 Unix 套接字路径 |
| `max_history_turns` | `20` | 每会话保留的最大对话轮数（一问一答为一轮） |

### `provider`

| 字段 | 默认值 | 说明 |
|---|---|---|
| `type` | `openai` | LLM 后端类型（当前支持 `openai` 兼容格式） |
| `base_url` | OpenAI 官方 | 替换为本地模型地址（见下方示例） |
| `api_key` | _(必填，仅 serve 模式)_ | API 密钥，或通过 `OPENAI_API_KEY` 环境变量传入 |
| `model` | `gpt-4o-mini` | 模型名称 |
| `system_prompt` | _(见下)_ | 系统提示词，注入至每个会话；支持模板变量（见下表） |
| `max_tokens` | `4096` | 单次补全最大 token 数 |
| `timeout_seconds` | `120` | LLM 请求超时时间（秒） |

**`system_prompt` 模板变量**（守护进程启动时展开，赋予模型运行环境感知能力）：

| 变量 | 展开为 |
|---|---|
| `{cwd}` | 当前工作目录 |
| `{os}` | 操作系统（`darwin` / `linux` / `windows`） |
| `{arch}` | CPU 架构（`amd64` / `arm64`…） |
| `{shell}` | `$SHELL` 环境变量（`/bin/zsh` 等） |
| `{home}` | 用户主目录 |
| `{user}` | 登录用户名 |
| `{hostname}` | 机器主机名 |
| `{datetime}` | 当前日期时间（`2006-01-02 15:04:05`） |
| `{date}` | 当前日期（`2006-01-02`） |

示例：

```yaml
provider:
  system_prompt: |
    You are a helpful assistant running on {os} ({arch}).
    The user's login is {user} and their home directory is {home}.
    The current working directory is {cwd}.
    They are using the shell {shell}.
    Today is {date}.
    Be concise and prefer code examples over long explanations.
```

### `cli`

CLI 客户端相关设置：

```yaml
cli:
  prompt: "You ❯ "                 # readline 提示符
  history_file: "~/.claw/history"  # 持久化输入历史；默认即此，留空同效
  shell_enabled: true              # 启用 !<cmd> 本地 shell 执行
  shell_timeout_seconds: 300       # shell 命令超时（0 = 无限制）
  allowed_commands:                # 命令白名单；留空 = 允许所有
    - git
    - python3
    - ls
```

---

## CLI 内置命令

在提示符下输入以下命令，或按 **Tab** 触发补全：

| 命令 | 说明 |
|---|---|
| `Tab` | 触发补全：`/` 命令、子命令、`!` 可执行文件名 |
| `!<cmd>` | 在本地执行 shell 命令，例如 `!git log`、`!vim file.txt` |
| `/reset` | 清除当前会话的对话历史（`main` 会话仅清空历史，不删除） |
| `/learn "<主题>"` | 扫描所有历史记忆，通过 Map-Reduce 提炼指定主题的经验，保存为 `.md` |
| `/exp ls` | 列出所有已保存的经验库 |
| `/exp show <主题>` | 查看某个经验库的内容 |
| `/exp use <主题>` | 将经验注入当前会话上下文（立即生效，增强后续回答） |
| `/exp rm <主题>` | 删除某个经验库 |
| `/help` | 显示帮助信息 |
| `/ml` | 进入多行输入模式，`/send` 发送，`/abort` 取消 |
| `exit` / `quit` | 断开连接并退出 |
| `\ + Enter` | 续行输入，下一行继续编辑，最终作为一条消息发送 |
| `^C` | 思考中：中断当前任务；输入状态空行：退出进程；输入状态有内容：取消本次输入 |
| `^D` | 退出（EOF） |

### Tab 补全说明

| 输入 | Tab 效果 |
|---|---|
| `/` | 列出所有内置命令 |
| `/exp ` | 列出子命令：`ls` `use` `show` `rm` |
| `/learn ` | 提示参数 `"<topic>"` |
| `!` | 从 `PATH` 搜索匹配的可执行文件名 |

> readline 行为：唯一匹配时直接补全；多个候选时再按一次 Tab 展开列表。

---

## 知识提炼与经验管理

### `/learn` — 提炼经验

```
You ❯ /learn "Linux 调试"
→ 开始提炼主题 Linux 调试 的经验知识…
  加载历史记忆…  →  关键词过滤 137 条记忆…  →  Map 1/3…  →  Reduce…
✓ 经验库已更新！共 42 行。使用 /exp show "Linux 调试" 查看。
```

**工作原理（Map-Reduce 流水线）：**

1. 扫描 `~/.claw/data/memory/` 下所有会话的历史记忆（JSONL）
2. 本地关键词评分过滤（无 LLM 开销），保留最相关的最多 80 条
3. 分批（每批 10 条）调用 LLM 提取要点（Map 阶段）
4. 将所有片段 + 旧版经验合并去重（Reduce 阶段）
5. 写入 `~/.claw/data/experiences/{主题}.md`

### `/exp use` — 挂载到会话

```
You ❯ /exp use "Linux 调试"
✓ 经验库 "Linux 调试" 已挂载到当前会话上下文

You ❯ strace 跑不起来怎么办？
Assistant: 根据之前的经验：...（使用挂载的知识回答）
```

挂载操作通过 IPC 向守护进程发送 `inject_ctx` 指令，以系统消息形式注入当前对话，不影响其他会话。

### 环境变量覆盖

| 变量 | 覆盖字段 |
|---|---|
| `OPENAI_API_KEY` | `provider.api_key` |

---

## 使用本地模型（Ollama）

```yaml
provider:
  type: openai
  base_url: "http://localhost:11434/v1"
  api_key: "ollama"    # 占位符，Ollama 会忽略
  model: "llama3.2"
```

其他兼容格式的服务（vLLM、LM Studio、LocalAI）同理，修改 `base_url` 和 `model` 即可。

---

## 目录结构

```
claw-go/
├── main.go              # 程序入口：serve/connect 模式切换、组件组装、优雅退出
├── config/
│   └── config.go        # YAML 配置结构、默认值、env 覆盖、校验
├── agent/
│   └── agent.go         # 消息分发核心：session → LLM agentic 循环 → channel
├── channel/
│   ├── channel.go       # Channel/InboundMessage/OutboundMessage 接口
│   └── socket.go        # Unix Domain Socket 频道（守护进程侧，单连接限制）
├── client/
│   ├── client.go        # 交互式 CLI 客户端（readline + 对话选择 UI + /learn /exp）
│   ├── completer.go     # Tab 补全：slash 命令 + shell 可执行文件名
│   ├── render.go        # Markdown 渲染（glamour）
│   ├── spinner.go       # 进度动画
│   ├── style.go         # 主题系统（Theme）
│   └── ui.go            # 对话选择界面、历史展示
├── dirs/
│   └── dirs.go          # 数据目录路径（sessions / logs / memory / experiences）
├── ipc/
│   └── ipc.go           # 协议帧（Msg / SessionInfo / HistoryEntry）+ 默认 socket 路径
├── knowledge/
│   ├── distill.go       # Map-Reduce 知识提炼流水线
│   ├── score.go         # 本地关键词评分 + FilterRelevant + FormatBatchForLLM
│   └── store.go         # ExperienceStore：CRUD Markdown 经验文件
├── memory/
│   ├── types.go         # TurnSummary / Action 结构
│   ├── extract.go       # 从 agent 输出中提取 TurnSummary
│   └── store.go         # Store（JSONL 追加写入）+ Manager（多会话）
├── provider/
│   ├── provider.go      # Provider 接口定义
│   └── openai.go        # OpenAI 兼容格式实现
├── session/
│   └── session.go       # 内存会话存储：命名对话、历史管理（并发安全）
├── startup/
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
