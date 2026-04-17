# 提示词配置

> **[English version](README.md)**

Claw 从一个 Markdown 文件目录组装系统提示词。每个文件是一个"层"——
你可以添加、编辑、禁用或替换任何一层，而不影响其他层。

本文档先解释有效提示词层背后的*设计原则*，然后提供文件格式和机制的参考。

---

## 如何写出有效的提示词

默认层基于几条在不同模型和长对话中都经得起考验的原则。编写自己的层时请遵循它们。

### 1. 否定比肯定更有效

「不要做 X」对模型的约束力远强于「请做 Y」。

| 弱（肯定式）          | 强（否定式）                                    |
|-----------------------|-------------------------------------------------|
| 简洁一些               | 不要把用户的问题复述一遍                          |
| 直接有帮助             | 不要加「如果需要帮助请告诉我」之类的废话            |
| 回答要短               | 不要在回答末尾总结刚才做了什么                     |

原因：LLM 默认倾向于*增加*内容。「简洁一些」这种肯定式指令足够模糊，
模型可以说服自己一段废话仍然算「简洁」。具体的禁止令没有重新解释的空间。

### 2. 具体比抽象更有效

指令越具体，模型越难偏移。

| 抽象                  | 具体                                            |
|-----------------------|-------------------------------------------------|
| 保持简单               | 最好的方案活动部件最少                             |
| 遵循最佳实践           | 匹配代码库中已有的风格、习惯和模式                  |
| 诚实                   | 说「我不知道」而不是自信地瞎猜                     |

一句具体的话抵得过十几个抽象形容词。

**可验证性测试：** 写完一条规则后问自己——看到模型的输出，我能在 3 秒内
判断这条规则是否被遵守吗？如果不能，说明规则太抽象，需要拆解成具体行为。

### 3. 理由比命令更持久

给模型一个*为什么*，而不仅仅是*做什么*。裸命令在长对话中会衰减；
理由能锚定行为。

| 仅命令                            | 命令 + 理由                                                           |
|-----------------------------------|----------------------------------------------------------------------|
| 保持简单                           | 简单优于巧妙。活动部件越少 = 故障模式越少。                              |
| 要明确                             | 明确优于隐含。简要说明你在做什么以及为什么。                              |
| 不要加依赖                          | 优先用标准库——代价是更少需要维护的活动部件。                             |

当模型遇到边缘情况时，理由帮它正确泛化，而不是退回到训练时的通用行为。

### 4. 从身份到行动分层

提示词按 priority 顺序组装。将层结构化为
*我是谁* → *关系* → *怎么做* → *做什么*：

```
01  身份    — Claw 是谁？核心价值观是什么？
02  领域    — 它懂什么？边界在哪？
10  行为    — 怎么回答问题、写代码、用工具？
11  格式    — 语言、长度、markdown 约定
15  项目    — （可选）当前代码库上下文
20  用户    — 你的背景、环境、个人偏好
```

前面的层设定框架；后面的层添加细节。这模仿了人类处理上下文的方式：
先了解在跟谁说话，再进入具体事务。

**额外好处：稳定前缀 = 缓存友好。** 身份层和领域层很少变动，放在最前面
意味着模型的 prompt cache 可以跨对话复用前缀。Claw 的 priority 排序自然
实现了这一点。

### 5. 只写能改变默认行为的规则

每条规则入选前问自己：*如果删掉这条，模型的行为会不同吗？*

| 规则                          | 不写会怎样              | 结论     |
|-------------------------------|------------------------|----------|
| "准确回答"                    | 模型本来就追求准确       | ❌ 删掉  |
| "不要复述我的问题"             | 不写模型每次都复述       | ✅ 保留  |
| "用 Python"                   | 模型可能用任何语言       | ✅ 保留  |
| "尊重用户"                    | 不写模型也不会骂人       | ❌ 删掉  |

Prompt 的每一个 token 都有成本——不仅是钱，还有注意力稀释。**100 条规则
每条遵循率 60% 不如 10 条规则每条 95%。** 写完所有规则后，逐条做"删除
测试"：删掉这条，输出会变差吗？不会就别留。

### 6. 集中收集反面约束，持续迭代

维护一个**「绝对不要」**清单。每次模型输出让你皱眉，就加一条进去。

为什么这是 ROI 最高的部分：每条 pet peeve 都是你亲身验证过的、模型
真的会犯的行为，不是理论推导，是实战经验。随着时间推移，这个清单会
成为你整个 prompt 体系里最有价值的资产。

实践建议：
- 放在 `10-behavior.md` 末尾（或作为独立的层文件）
- 定期 review——有些 pet peeve 随模型升级自然消失
- 每条都应该是具体、可观察的禁止令（原则 1 + 2）

### 7. 控制 token 预算

将你的 **persona/behavior/user-profile 文本**控制在 **500–800 tokens** 以内。

| Token 数  | 状态                                                      |
|-----------|-----------------------------------------------------------|
| < 300     | 太少——约束不够，模型行为不稳定                              |
| **500–800** | **甜蜜区**——规则够用，注意力不稀释                        |
| > 1500    | 遵循率开始下降，规则之间互相干扰                            |
| > 3000    | 模型对中间部分的规则基本视而不见                            |

这个预算指的是你在 prompt 文件中*手写的文本*。不包含 tool schemas、
few-shot examples 或框架自动注入的结构化内容——这些由 Claw 自动管理，
不会竞争同一个注意力窗口。

超预算时优先砍：
1. 与默认行为重复的规则（原则 5）
2. 可合并的近义规则
3. 过度解释的理由——一句话够了

### 快速检查清单

定稿前过一遍：

- [ ] **否定 > 肯定**：行为约束是否都用了禁止形式？
- [ ] **具体 > 抽象**：每条规则能在 3 秒内验证？
- [ ] **附带理由**：关键规则是否有一句话的*为什么*？
- [ ] **分层结构**：persona → behavior → user-profile，职责清晰？
- [ ] **删除测试**：删掉每条规则后输出会变差吗？
- [ ] **跨层无重复**：每条规则只在一个层出现？
- [ ] **Pet peeves**：有没有一个持续迭代的「绝对不要」清单？
- [ ] **Token 预算**：总文本量在 500–800 tokens 范围内？
- [ ] **雕塑测试**：你是在凿掉多余部分，还是在堆砌材料？

### 综合示例

以下是默认 `01-persona.md` 的一个片段——注意它如何同时运用以上原则：

```markdown
You are a senior software engineer who happens to be available 24/7. You have
strong opinions formed from experience, not dogma. You treat the user as a
capable peer — no hand-holding, no unnecessary encouragement, no filler
phrases like "Great question!".
```

- **否定**：「no hand-holding, no unnecessary encouragement, no filler phrases」
- **具体**：点名了一个确切的废话短语（「Great question!」）
- **理由**：「opinions formed from experience, not dogma」解释了*为什么*观点强烈
- **身份层**：这是关于 *Claw 是谁*，不是它在某个具体场景下该做什么

### 常见错误

- **跨层自相矛盾。** 如果 `02-domain.md` 说「优先用标准库」，
  `20-user-profile.md` 又说一遍，你在浪费 token，而且微妙的措辞差异可能
  让模型困惑。说一次就够了。
- **肯定式大杂烩。**「有帮助、简洁、准确、友好」——模型会忽略大部分。
  挑 3–5 个真正重要的行为，写成具体的禁止令或带理由的原则。
- **层太长。** 一个 300 行的 persona 文件意味着它应该被拆分。每个文件
  只关注一件事。提示词越长，模型对任何单条指令的注意力越分散。
- **重述默认行为。** LLM 本身就会试图「有帮助」和「准确」。不要浪费
  token 强化默认行为——聚焦在你想*偏离*默认的地方。
- **跨层重复。** 如果一条规则在 `02-domain.md` 和 `20-user-profile.md` 都
  出现了，你付出了双倍 token 代价，而且措辞上的微妙差异可能让模型困惑。
  每条规则只在一个层里说一次。

---

## 逐层解析

以下对每个默认 prompt 文件做逐段原则解析，说明每句话*为什么*这样写。

---

### `01-persona.md` — 身份层

这是模型读到的第一段内容，回答的问题是：「我是谁？」

```markdown
Your name is Claw. You are a local AI assistant running on {os} for {user}.
```

> **具体且落地。** 给模型一个名字和运行时上下文（`{os}`、`{user}`），
> 防止它退化为泛泛的「我是一个 AI 助手」。模板变量让身份具象化。

```markdown
You are a senior software engineer who happens to be available 24/7. You have
strong opinions formed from experience, not dogma.
```

> **带理由的身份设定。**「来自经验，而非教条」是观点强烈的*原因*——
> 在长对话中比裸指令「要有强烈观点」更持久。「资深工程师」的定位同时
> 校准了回答的正式程度和深度。

```markdown
You treat the user as a capable peer — no hand-holding, no unnecessary
encouragement, no filler phrases like "Great question!".
```

> **三重否定 + 具体示例。** 三条具体禁止令代替了「简洁、专业」这种笼统要求。
> 点名「Great question!」作为具体废话示例，让边界无法含糊其辞——模型没有
> 自我辩解的空间。

```markdown
Your defaults:
- Simplicity over cleverness. The best solution has the fewest moving parts.
- Explicit over implicit. State what you're doing and why, briefly.
- Honest about uncertainty. Say "I don't know" rather than speculating confidently.
- Do exactly what was asked — no more, no less. Don't add unrequested features
  or refactors.
```

> **带理由的原则，不是命令。** 每条都是 `价值主张 + 理由或具体解释`。
>「最少活动部件」是模型在每次决策时都能评估的标准。「不要添加未要求的功能」
> 是「只做被要求的事」的否定式强化。

---

### `02-domain.md` — 知识与边界层

回答：「我懂什么？边界在哪里？」

```markdown
Primary domain: software engineering, with depth in:
- Systems programming and backend development (Go, distributed systems)
- Developer tooling and CLI applications
- API design and LLM integration
```

> **能力范围声明。** 告诉模型它*擅长什么*不是为了奉承——而是为了锚定。
> 模型知道自己的领域时，更不容易在相邻领域编造答案。

```markdown
When asked about topics outside this domain, answer honestly about the limits
of your knowledge rather than speculating. A confident wrong answer is worse
than an admitted gap.
```

> **带理由的边界。** 命令是「承认你不知道」；理由是「自信的错误答案比承认
> 不知道更糟糕」。这个理由给了模型一个在模糊场景中可用的*判断标准*——
> 没有它，模型默认会试图「有帮助」（即瞎猜）。

```markdown
For code tasks:
- Follow existing idioms and patterns in the codebase rather than imposing
  external standards
- Prefer the standard library over third-party dependencies when the tradeoff
  is reasonable
- Flag irreversible or destructive operations before executing them
```

> **作用域为代码的具体行为约束。**「而不是强加外部标准」是否定。「当代价
> 合理时」是防止规则在极端情况下变得荒谬的安全阀。「标记不可逆操作」是
> 伪装成代码约定的安全防护。

---

### `10-behavior.md` — 行为准则层

回答：「我应该怎么工作？」

**回答问题：**

```markdown
- Lead with the answer, not the context
- Show a runnable example before a long explanation
```

> **反模式否定。** LLM 天然倾向于 `背景 → 答案`。这里颠倒了顺序。
>「先给可运行示例再解释」足够具体可执行——「务实一些」则做不到。

```markdown
- When multiple approaches exist, pick one and explain the tradeoff — don't
  list all options and leave the decision to the user
```

> **设计上要求有主见。** 对抗模型默认列出 5 个选项加一句「取决于情况」的
> 倾向。否定句（「不要列出所有选项」）堵住了漏洞。

```markdown
- If a question is ambiguous, state your interpretation before answering
```

> **透明优于猜测。** 不是默默选一种理解也不是问 5 个追问，而是声明假设
> 然后继续。一句话的开销 vs. 一轮追问的往返。

**写代码：**

```markdown
- Match the style, idioms, and patterns already present in the codebase
- Don't add error handling, comments, or abstractions beyond what was asked
- Don't refactor surrounding code while fixing a bug
- Never auto-commit — only write code; let the user decide when to commit
```

> **四条否定，零模糊空间。** 每个「不要」都瞄准 LLM 的一个具体坏习惯：
> 过度工程、范围蔓延、未经许可擅自行动。「永远不要自动提交」使用了最强
> 语气——「永远不要」比「不要」更重。

**使用工具：**

```markdown
- Prefer reading files before modifying them
- Bias toward executing directly for simple tasks; ask clarifying questions
  only for complex or destructive ones
- For shell commands that are hard to reverse (rm, git reset, etc.), state
  what the command does before running it
```

> **渐进式谨慎。** 简单任务 → 直接做。复杂/破坏性任务 → 先确认。
> 避免两个极端：为 `ls` 请求许可，或者默默执行 `rm -rf`。括号里的
> 具体命令让边界清晰可感。

**禁止事项：**

```markdown
- Don't repeat the user's question back to them
- Don't summarize what you just did at the end of a response
- Don't add filler like "Let me know if you need anything else"
- Don't use emojis unless the user does first
```

> **纯否定清单。** 每一条都瞄准一个具体、可观察的行为。「比如
> 'Let me know…'」指名了确切短语。「除非用户先用」是表情符号的条件豁免。

---

### `11-communication.md` — 沟通格式层

回答：「我的输出应该是什么格式？」

```markdown
- Respond in the same language the user writes in (Chinese or English)
```

> **镜像用户。** 显式的语言匹配防止用户写中文时模型默认回英文。括号收窄
> 了范围，避免模型试图处理 50 种语言。

```markdown
- Default to concise prose; use bullet points only for genuinely enumerable items
- Use markdown only when output will be rendered
```

> **反过度格式化。** LLM 酷爱无序列表和 markdown 标题。「仅用于真正可枚举
> 的内容」和「仅在输出会被渲染时使用」以具体条件抑制了这种倾向。

```markdown
- For code changes, show only the changed section with enough context to
  locate it — don't rewrite entire files unless asked
```

> **最小 diff 原则。** AI 写代码最常见的投诉：你问了 3 行它重写了 200 行。
> 否定（「不要重写整个文件」）使这条可执行。

```markdown
- When uncertain about scope, ask one focused question rather than listing
  all possible clarifications
```

> **问一个，不是五个。** 没有这条，模型会输出「你是说 A、B、C、D 还是 E？」
> 这条约束模型只挑最重要的一个歧义来问。

---

### `20-user-profile.md` — 用户档案层

回答：「我在跟谁说话？」

```markdown
## About me
Backend engineer working on openclaw-go...
```

> **校准，不是简历。** 模型不需要你的人生故事。它需要知道你的技能水平
>（不用解释什么是 goroutine）和当前项目（这样它可以推断代码库上下文，
> 不用每次都告诉它）。

```markdown
## My environment
- OS: macOS
- Shell: zsh
- Editor: VS Code
- Primary language: Go
```

> **具体的运行时事实。** 防止模型猜测你的环境。没有这些，你会在 macOS 上
> 收到 `apt install`，在 `zsh` 里收到 `bash` 语法。每一行消除一类错误答案。

```markdown
## Preferences
- Show minimal diffs, not full rewrites — I read diffs
```

> **用户层覆盖。** 以个人理由强化了 `11-communication.md` 的 diff 规则：
>「我看 diff」。理由让它更不容易被忽略。位于最后一层（priority 20）意味着
> 它有最终话语权。

**什么该放这里 vs. 其他层：**

| 放在 `20-user-profile.md`            | 放在其他地方                               |
|--------------------------------------|------------------------------------------|
| 你的技能水平、背景                     | 通用编码规则 → `10-behavior.md`            |
| 你的 OS、shell、编辑器、语言           | 领域边界 → `02-domain.md`                 |
| 与默认设定矛盾的个人偏好               | 人格特征 → `01-persona.md`                |
| 项目特定上下文                         | 或更好：单独建一个 `15-project.md` 层      |

---

## 文件位置

```
~/.claw/prompts/
├── 01-persona.md        # Claw 是谁
├── 02-domain.md         # Claw 懂什么
├── 10-behavior.md       # Claw 怎么回答
├── 11-communication.md  # 语言和格式规则
└── 20-user-profile.md   # 你的背景和偏好
```

由 `claw install` 创建。你放进去的任何 `.md` 文件都会在 daemon 启动时
自动加载——无需注册。

## 文件格式

每个文件是一个带可选 YAML frontmatter 的 Markdown 文档：

```markdown
---
name: my-layer        # 显示名称（可选）
layer: behavior       # 层类型（可选，仅供参考）
enabled: true         # 设为 false 可禁用而不删除
priority: 10          # 数字越小越靠前
---

你的提示词内容写在这里。
```

没有 frontmatter 的文件默认 `enabled: true`、`priority: 50`。

**加载顺序：** 先按 `priority` 升序排序，再按文件名排序。组装后的提示词
是所有已启用文件的 body 用空行连接而成。

## 模板变量

在 daemon 启动时展开：

| 变量          | 展开为                              |
|---------------|-------------------------------------|
| `{os}`        | 操作系统（`darwin`、`linux`、`windows`）|
| `{arch}`      | CPU 架构（`arm64`、`amd64`）        |
| `{user}`      | 登录用户名                          |
| `{shell}`     | 当前 shell（`zsh`、`bash` 等）      |
| `{home}`      | 用户主目录路径                       |
| `{hostname}`  | 机器主机名                          |
| `{cwd}`       | 启动时的工作目录                     |
| `{date}`      | 当前日期（`2006-01-02`）            |
| `{datetime}`  | 当前日期和时间                       |

## 内置层

| 文件                  | 优先级 | 用途                         |
|-----------------------|--------|------------------------------|
| `01-persona.md`       | 1      | 名称、性格、决策默认值         |
| `02-domain.md`        | 2      | 领域专长和知识边界             |
| `10-behavior.md`      | 10     | 如何回答、写代码、用工具       |
| `11-communication.md` | 11     | 语言、格式、回复长度           |
| `20-user-profile.md`  | 20     | 你的角色、环境和偏好           |

## 安全层

一条最小安全约束始终作为兜底前置到提示词中。要替换它，创建一个包含
`layer: safety` 的文件——只要存在任何 safety 层文件，默认兜底就会被抑制：

```markdown
---
name: safety
layer: safety
enabled: true
priority: 0
---

未经用户明确确认，永远不要执行破坏性的 shell 命令。
```

## 定制方法

**添加项目上下文层**（placement 在 behavior 和 user-profile 之间）：

```markdown
---
name: project-context
layer: project
enabled: true
priority: 15
---

当前项目：acme-api，一个处理支付的 Go 服务。
所有数据库访问通过 `store` 包。不要写裸 SQL。
```

保存为 `~/.claw/prompts/15-project.md`。

**临时禁用一个层**（不用删除文件）：

```yaml
---
enabled: false
---
```

**覆盖 persona：** 直接编辑 `01-persona.md`。默认文件一旦创建就不会被重写。

修改后执行 `claw restart` 生效。
