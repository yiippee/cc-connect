# cc-connect 项目深度分析

> **版本:** v1.2.1 | **Go:** 1.24.2 | **模块:** `github.com/chenhg5/cc-connect`
> **规模:** 84 个 Go 文件，约 11,141 行代码，20 个测试文件

---

## 一、项目概述

cc-connect 是一个消息桥接服务，将本地 AI 编程助手（Claude Code、Cursor、Gemini CLI、Codex 等）连接到多种即时通讯平台（飞书、钉钉、Telegram、Slack、Discord、LINE、企业微信、QQ）。大部分平台采用 WebSocket/长轮询方式，无需暴露公网 URL。

**核心价值：** 开发者可以直接在聊天应用中与 AI Agent 交互，无需切换到终端。

---

## 二、项目结构总览

```
cc-connect/
├── cmd/cc-connect/          # CLI 入口与子命令
│   ├── main.go              # 主入口：配置加载、引擎初始化、信号处理
│   ├── daemon.go            # daemon install/uninstall/start/stop/logs
│   ├── send.go              # send 子命令（通过 Unix Socket IPC）
│   ├── cron.go              # cron add/list/del 子命令
│   ├── provider.go          # provider add/list/remove/import
│   ├── relay.go             # relay send 子命令
│   └── update.go            # update/check-update
├── core/                    # 核心抽象层与路由引擎
│   ├── interfaces.go        # Platform / Agent / AgentSession 接口定义
│   ├── registry.go          # 工厂注册模式（RegisterPlatform / RegisterAgent）
│   ├── message.go           # 统一消息类型 & 事件类型
│   ├── engine.go            # 主路由引擎：消息分发、事件处理、权限管理
│   ├── session.go           # 多会话管理器，持久化存储
│   ├── command.go           # 自定义命令 & 别名注册表
│   ├── skill.go             # Skill 注册与调用
│   ├── speech.go            # 语音转文字（OpenAI/Groq/Qwen Whisper）
│   ├── tts.go               # 文字转语音（OpenAI/Qwen）
│   ├── cron.go              # 定时任务调度器
│   ├── relay.go             # Bot 间中继通信
│   ├── api.go               # Unix Socket HTTP API
│   ├── i18n.go              # 国际化（en/zh/zh-TW/ja/es）
│   ├── dedup.go             # 消息去重
│   ├── ratelimit.go         # 每会话速率限制
│   ├── markdown.go          # Markdown → HTML 转换
│   ├── streaming.go         # 流式预览（原地更新消息）
│   ├── atomicwrite.go       # 原子文件写入
│   ├── doctor.go            # 系统诊断命令
│   ├── updater.go           # 自更新功能
│   └── *_test.go            # 各模块测试
├── agent/                   # Agent 适配器实现
│   ├── claudecode/          # Claude Code CLI
│   ├── cursor/              # Cursor Agent CLI
│   ├── gemini/              # Google Gemini CLI
│   ├── codex/               # OpenAI Codex CLI
│   ├── qoder/               # Qoder CLI
│   ├── opencode/            # OpenCode CLI
│   └── iflow/               # iFlow CLI
├── platform/                # 平台适配器实现
│   ├── feishu/              # 飞书（WebSocket）
│   ├── dingtalk/            # 钉钉（Stream SDK）
│   ├── telegram/            # Telegram（长轮询）
│   ├── slack/               # Slack（Socket Mode）
│   ├── discord/             # Discord（Gateway WebSocket）
│   ├── line/                # LINE（Webhook）
│   ├── wecom/               # 企业微信（Webhook + AES 加密）
│   ├── qq/                  # QQ（OneBot v11 WebSocket）
│   └── qqbot/               # QQ Bot（官方 API v2 WebSocket）
├── config/                  # 配置加载与持久化
│   └── config.go            # TOML 配置结构、热重载、原子写入
├── daemon/                  # 系统服务管理
│   ├── manager.go           # Daemon 接口与元数据管理
│   ├── systemd.go           # Linux systemd
│   ├── launchd.go           # macOS launchd
│   ├── logrotate.go         # 日志轮转
│   └── unsupported.go       # Windows / 其他（不支持）
├── config.example.toml      # 完整配置模板
├── embed.go                 # 嵌入 config.example.toml
├── Makefile                 # 构建目标
├── go.mod                   # 依赖声明
└── README.md                # 项目文档
```

---

## 三、核心架构

### 3.1 消息流转管线

```
用户消息 → Platform 接收 → MessageHandler → Engine 路由
    ├─ 语音消息? → STT 转文字 → 重新分发
    ├─ 命令消息? → handleCommand() → 执行内置/自定义/Skill 命令
    ├─ 权限响应? → 解析 allow/deny → 恢复等待中的权限请求
    └─ 普通消息? → processInteractiveMessage()
         ↓
    Session.TryLock() — 防止并发处理同一会话
         ↓
    Agent.StartSession() 或复用已有会话
         ↓
    AgentSession.Send(message, images)
         ↓
    processInteractiveEvents() — 事件循环
         ├─ EventThinking    → 发送推理过程预览（可配置截断长度）
         ├─ EventToolUse     → 发送工具调用预览
         ├─ EventToolResult  → 发送工具执行结果
         ├─ EventText        → 累积文本（支持流式预览原地更新）
         ├─ EventPermissionRequest → 显示按钮，等待用户响应
         ├─ EventResult      → 最终响应 + 历史记录 + TTS
         └─ EventError       → 发送错误信息
         ↓
    Session.AddHistory() → SessionManager.Save()
    Session.Unlock()
         ↓
    Platform.Reply() → 用户收到回复
```

### 3.2 插件式工厂注册模式

```go
// core/registry.go — 中央注册表
RegisterPlatform(name string, factory PlatformFactory)
RegisterAgent(name string, factory AgentFactory)
CreatePlatform(name string, opts map[string]any) (Platform, error)
CreateAgent(name string, opts map[string]any) (Agent, error)
```

每个 Agent 和 Platform 在 `init()` 中自注册：

```go
// agent/claudecode/claudecode.go
func init() { core.RegisterAgent("claudecode", New) }

// platform/feishu/feishu.go
func init() { core.RegisterPlatform("feishu", New) }
```

`cmd/cc-connect/main.go` 通过空白导入触发注册。添加新 Agent/Platform 只需实现接口、注册、添加空白导入。

### 3.3 核心接口体系

**基础接口：**

| 接口 | 方法 | 职责 |
|------|------|------|
| `Platform` | Name, Start, Reply, Send, Stop | 消息平台抽象 |
| `Agent` | Name, StartSession, ListSessions, Stop | AI Agent 抽象 |
| `AgentSession` | Send, RespondPermission, Events, Close, Alive | 运行中的交互会话 |

**可选接口（按需实现）：**

| 接口 | 职责 | 实现者 |
|------|------|--------|
| `ProviderSwitcher` | 多 API Provider 切换 | claudecode, cursor, gemini, codex, opencode, iflow |
| `MemoryFileProvider` | 指令文件路径（CLAUDE.md 等） | claudecode, gemini, codex, qoder, opencode, iflow |
| `ModelSwitcher` | 运行时模型切换 | claudecode, gemini, codex, opencode |
| `ModeSwitcher` | 权限模式切换 | claudecode, cursor, gemini, codex, qoder, iflow |
| `ContextCompressor` | 上下文压缩 | claudecode(/compact), gemini(/compress), codex, qoder, opencode, iflow |
| `CommandProvider` | 自定义命令目录 | claudecode, gemini |
| `SkillProvider` | Skill 目录 | claudecode, cursor, gemini, codex, qoder |
| `ToolAuthorizer` | 动态工具白名单 | claudecode |
| `SessionDeleter` | 删除会话 | claudecode |
| `HistoryProvider` | 获取会话历史 | claudecode, codex |
| `SystemPromptSupporter` | 原生系统提示注入 | claudecode |
| `SessionEnvInjector` | 注入会话环境变量 | 所有 Agent |
| `TypingIndicator` | 打字状态指示 | feishu, telegram, discord |
| `MessageUpdater` | 消息原地更新 | feishu, telegram, discord |
| `InlineButtonSender` | 内联按钮 | telegram |
| `CommandRegistrar` | 平台命令菜单注册 | telegram, discord |
| `ReplyContextReconstructor` | 重建回复上下文（用于定时任务） | 所有 Platform |

### 3.4 并发模型

- 消息处理在独立 goroutine 中运行，handler 立即返回
- `sync.Mutex` 保护会话状态（interactiveMu, bannedMu, aliasMu）
- `sync.RWMutex` 保护读多写少的数据（commands, aliases, providers）
- `Session.TryLock()` 非阻塞锁防止同一会话并发处理
- TTS 回复异步发送：`go e.sendTTSReply(...)`
- 原子文件写入防止配置/会话数据损坏

---

## 四、Agent 适配器详解

### 4.1 通用模式

所有 Agent 适配器共享以下模式：
- 通过子进程启动对应 CLI 工具
- 输出格式统一为 `stream-json`（NDJSON）
- 解析 JSON 事件流，转换为统一的 `core.Event`
- 通过环境变量注入 API Key、Base URL 等配置

### 4.2 各 Agent 对比

| Agent | CLI 工具 | 进程模型 | PTY | 会话存储 | 权限模型 |
|-------|---------|---------|-----|---------|---------|
| **claudecode** | `claude` | 持久进程 | 否 | JSONL (~/.claude/projects/) | 请求/响应式 control 事件 |
| **cursor** | `agent` (npm) | 每轮新进程 | 否 | SQLite (store.db) | CLI 标志 (--trust/--force) |
| **gemini** | `gemini` (npm) | 每轮新进程 | 否 | JSON (~/.gemini/tmp/) | CLI 标志 (-y/--approval-mode) |
| **codex** | `codex` (npm) | 每轮新进程 | 否 | JSONL (~/.codex/sessions/) | CLI 标志 (--full-auto) |
| **qoder** | `qodercli` | 每轮新进程 | 否 | 内部存储 | CLI 标志 (--dangerously-skip-permissions) |
| **opencode** | `opencode` | 每轮新进程 | 否 | 内部存储 | CLI 标志 |
| **iflow** | `iflow` | 每轮新进程 | **是** | JSONL (~/.iflow/projects/) | 交互式 PTY |

### 4.3 Claude Code Agent（最完整的实现）

**启动方式：**
```
claude --output-format stream-json --input-format stream-json --permission-prompt-tool stdio
      [--resume <sessionID>] [--model <model>] [--permission-mode <mode>]
      [--append-system-prompt <prompt>] [--allowedTools <tools>]
```

**特点：**
- **唯一持久进程模型**：进程保持运行，通过 stdin/stdout JSON 流双向通信
- **权限请求/响应**：收到 `control_request` 事件后等待用户 allow/deny
- **Provider 代理**：内置 HTTP 代理重写 thinking 参数，兼容非 Anthropic 端点
- **图片支持**：Base64 编码 + 本地文件引用
- **系统提示注入**：通过 `--append-system-prompt` 注入 cron/relay 指令
- **指令文件**：CLAUDE.md（项目级和全局级）
- **命令/Skill 目录**：`.claude/commands/`、`.claude/skills/`

**权限模式：** default, acceptEdits, plan, bypassPermissions

### 4.4 Cursor Agent

**启动方式：**
```
agent --print --output-format stream-json --trust [--resume <chatID>]
      [--force] [--mode plan|ask]
```

**特点：**
- SQLite 会话存储，Protobuf-like 二进制 blob 编码
- Thinking 内容被抑制（太冗长）
- 工具名称映射：shellToolCall → Bash, readToolCall → Read 等
- Skill 目录：`.claude/skills/`

### 4.5 Gemini Agent

**启动方式：**
```
gemini -p <prompt> --output-format stream-json [--resume <chatID>]
       [-y] [--approval-mode auto_edit|plan]
```

**特点：**
- 图片支持：通过 `@file` 引用临时文件
- 从 Google generativelanguage API 获取可用模型列表
- 命令/Skill 目录：`.gemini/commands/`、`.gemini/skills/`
- 指令文件：GEMINI.md

### 4.6 Codex Agent

**启动方式：**
```
codex exec --json --skip-git-repo-check [resume <threadID>]
      [--full-auto] [--dangerously-bypass-approvals-and-sandbox]
```

**特点：**
- Session patching：重写 source 字段使会话在交互模式可见
- 按工作目录过滤会话列表
- 指令文件：AGENTS.md（位于 `~/.codex`）

### 4.7 iFlow Agent（最复杂的实现）

**启动方式：**
```
iflow -i <prompt> [-r <sessionID>] [-c]
      [--default|--autoEdit|--plan|--yolo]
```

**特点：**
- **唯一使用 PTY** 的 Agent（`github.com/creack/pty`）
- 混合交互：PTY stdin/stdout + JSONL transcript 轮询
- ANSI 转义码剥离
- 复杂的工具挂起超时逻辑（default 模式 6s，其他模式 45s）
- 会话目录符号链接解析

---

## 五、Platform 适配器详解

### 5.1 各平台对比

| 平台 | 连接方式 | 打字指示 | 消息更新 | 按钮 | 命令注册 | 语音 | 图片 |
|------|---------|---------|---------|------|---------|------|------|
| **飞书** | WebSocket (SDK) | emoji 反应 | 卡片更新 | — | — | opus (ffmpeg) | 是 |
| **钉钉** | Stream SDK | — | — | — | — | — | — |
| **Telegram** | 长轮询 | typing action | 编辑消息 | 内联键盘 | setMyCommands | ogg/mp3 | 是 |
| **Slack** | Socket Mode | — | — | — | — | 是 | 是 |
| **Discord** | Gateway WS | typing | 编辑消息 | — | 斜杠命令 | 是 | 是 |
| **LINE** | Webhook | — | — | — | — | m4a | 是 |
| **企业微信** | Webhook + AES | — | — | — | — | amr/speex | 是 |
| **QQ** | OneBot v11 WS | — | — | — | — | silk/amr/mp3 | 是 |
| **QQ Bot** | 官方 API v2 WS | — | — | — | — | — | 是 |

### 5.2 飞书 (Feishu)

**连接：** 通过官方 SDK 的 `larkws` WebSocket 客户端连接。

**消息格式化（三级渲染策略）：**
1. 含代码块/表格 → 交互卡片（Schema 2.0 Markdown）
2. 含多段空行分隔 → Post 富文本格式
3. 其他 Markdown → Post + md 标签

**特殊功能：**
- 打字指示器通过表情反应实现（默认 "OnIt" emoji）
- 支持 `reply_in_thread` 话题回复
- 支持 `share_session_in_channel` 频道共享会话
- 音频需 ffmpeg 转 opus 格式

### 5.3 Telegram

**连接：** 30 秒超时的长轮询。

**消息格式化：** Markdown → Telegram HTML（`<b>`, `<i>`, `<s>`, `<code>`, `<pre>`, `<a>`, `<blockquote>`），解析失败回退纯文本。

**特殊功能：**
- 内联键盘按钮（用于权限请求等交互）
- 命令菜单注册（max 100 条，描述 max 256 字符）
- 代理支持（可选认证）
- 消息长度限制 2000 字符，按换行拆分
- 群组/私聊过滤，@提及检测

### 5.4 Discord

**连接：** Gateway WebSocket + 应用命令（Slash Commands）。

**特殊功能：**
- 20+ 内置斜杠命令（help, new, list, switch, model 等），带选项参数
- 交互响应：延迟响应 + 后续消息
- 命令注册支持 Guild 级别和全局级别
- 消息长度限制 2000 字符，代码围栏感知拆分

### 5.5 企业微信 (WeCom)

**连接：** Webhook（HTTP POST），AES-256-CBC 加密回调。

**特殊功能：**
- SHA1 签名验证 + AES-256-CBC 解密 + PKCS#7 填充
- Access Token 缓存（带过期跟踪）
- 消息按字节长度拆分（2048 字节限制，UTF-8 安全）
- 可选 Markdown 支持（`enable_markdown` 配置）

### 5.6 QQ (OneBot v11)

**连接：** 正向 WebSocket（OneBot v11 协议），连接到 NapCat 等实现。

**特殊功能：**
- echo 序列路由的 API 调用机制
- 指数退避重连（最多 30 次）
- CQ 码消息格式解析
- 支持 SILK/AMR/MP3 音频格式

### 5.7 QQ Bot (官方 API v2)

**连接：** WebSocket Gateway，OAuth2 Token 自动刷新。

**特殊功能：**
- OAuth2 Token 自动续期（5 分钟提前量）
- 心跳 + 会话恢复机制
- 被动回复 `msg_seq` 跟踪（5 分钟 TTL）
- 沙箱模式支持

---

## 六、核心模块详解

### 6.1 Engine（core/engine.go）

核心路由引擎，协调消息在 Platform ↔ Agent 之间的流转。

**关键数据结构：**

```go
type Engine struct {
    name              string
    agent             Agent
    platforms         []Platform
    sessions          *SessionManager
    commands          *CommandRegistry
    skills            *SkillRegistry
    aliases           map[string]string
    interactiveStates map[string]*interactiveState
    cronScheduler     *CronScheduler
    relayManager      *RelayManager
}

type interactiveState struct {
    agentSession  AgentSession
    platform      Platform
    replyCtx      any
    pending       *pendingPermission
    approveAll    bool    // /allow all 后全部自动通过
    quiet         bool    // 静默模式
    fromVoice     bool    // 是否来自语音转文字
}
```

**内置命令（28+）：**
`/new`, `/list`, `/switch`, `/del`, `/history`, `/model`, `/mode`, `/lang`, `/quiet`, `/provider`, `/cron`, `/bind`, `/shell`, `/tts`, `/memory`, `/reload`, `/restart`, `/help`, `/doctor`, `/update`, `/compact`, `/version`, `/allow`, `/deny`, `/skill`, `/cmd` 等。

**命令匹配：** 支持前缀匹配，如 `/pro l` → `/provider list`。

**关键常量：**
- `maxPlatformMessageLen = 4000`（消息分片大小）
- `slowPlatformSend = 2s`, `slowAgentStart = 5s`, `slowAgentFirstEvent = 15s`
- `defaultEventIdleTimeout = 2 小时`

### 6.2 SessionManager（core/session.go）

**会话标识：** `sessionKey` 格式为 `{platform}:{chatID}:{userID}`

```go
type Session struct {
    ID             string         // 本地会话 ID (s1, s2, ...)
    Name           string         // 用户自定义名称
    AgentSessionID string         // Agent 后端会话 ID
    History        []HistoryEntry // 本地消息历史
    CreatedAt      time.Time
    UpdatedAt      time.Time
    mu             sync.Mutex
    busy           bool           // TryLock 机制
}

type SessionManager struct {
    sessions      map[string]*Session      // sessionID → Session
    activeSession map[string]string        // userKey → 活跃 sessionID
    userSessions  map[string][]string      // userKey → sessionID 列表
    storePath     string                   // JSON 持久化路径
}
```

**持久化：** JSON 文件，使用深拷贝快照 + 原子写入防止竞态条件。

### 6.3 CommandRegistry（core/command.go）

```go
type CustomCommand struct {
    Name        string  // 不含前缀 "/"
    Description string
    Prompt      string  // 模板，支持 {{1}}, {{2}}, {{args}}, {{1*}}, {{1:default}}
    Exec        string  // Shell 命令（与 Prompt 互斥）
    WorkDir     string
    Source      string  // "config" 或 "agent"
}
```

**模板占位符：**
- `{{1}}`, `{{2}}` — 位置参数
- `{{1:default}}` — 带默认值
- `{{2*}}` — 第 N 个及之后所有参数
- `{{args}}` — 全部参数拼接
- `{{args:default}}` — 带默认值的全部参数

### 6.4 Skill 系统（core/skill.go）

```go
type Skill struct {
    Name        string  // 子目录名
    DisplayName string  // 来自 YAML frontmatter
    Description string
    Prompt      string  // 指令内容
    Source      string  // Skill 目录路径
}
```

发现机制：扫描 Agent 的 Skill 目录，查找 `{skilldir}/{name}/SKILL.md`，解析可选的 YAML frontmatter。

### 6.5 定时任务（core/cron.go）

```go
type CronJob struct {
    ID, Project, SessionKey string
    CronExpr    string      // 5 字段标准 cron
    Prompt      string
    Description string
    Enabled     bool
    Silent      *bool
    LastRun     time.Time
    LastError   string
}
```

**执行流程：**
1. 用户通过 `/cron add` 或 Agent 调用 `cc-connect cron add` 添加任务
2. 调度器在预定时间注入合成消息到 Engine
3. 消息像用户发送的一样被处理
4. 每任务 30 分钟超时
5. 支持人类可读 cron 表达式（多语言）

### 6.6 Relay 中继（core/relay.go）

```go
type RelayBinding struct {
    Platform string
    ChatID   string
    Bots     map[string]string // project → 显示名
}
```

**流程：** 用户 `/bind` 绑定群聊中的多个项目 → Agent 通过 `cc-connect relay send --to <project> "<message>"` 发送中继消息 → 目标 Engine 处理并回复 → 双方响应在群聊可见。

### 6.7 语音处理

**STT Provider：**
- OpenAI Whisper（兼容 Groq）
- Qwen ASR（阿里 DashScope）
- 需要 ffmpeg 转换不兼容格式（amr, ogg, silk → mp3）

**TTS Provider：**
- OpenAI TTS
- Qwen TTS（阿里 DashScope）
- 模式：`voice_only`（仅语音消息回复语音）/ `always`（始终附带语音）

### 6.8 国际化（core/i18n.go）

支持语言：en, zh, zh-TW, ja, es + 自动检测。

自动检测逻辑：扫描文本字符 — 平假名/片假名 → 日语，CJK 字符 → 中文，ñ/¿/¡ → 西班牙语，默认英语。

100+ 消息键，每个键有所有语言的翻译。

### 6.9 Unix Socket API（core/api.go）

**端点：**

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/send` | 发送消息到活跃会话 |
| GET | `/sessions` | 列出所有引擎的活跃会话 |
| POST | `/cron/add` | 添加定时任务 |
| GET | `/cron/list?project=X` | 列出定时任务 |
| POST | `/cron/del` | 删除定时任务 |
| POST | `/relay/send` | 中继消息 |
| POST | `/relay/bind` | 绑定群聊 |
| GET | `/relay/binding?chat_id=X` | 查询绑定 |

Socket 路径：`{dataDir}/run/api.sock`

### 6.10 消息去重（core/dedup.go）

- 60 秒窗口内跟踪消息 ID
- 丢弃进程启动前的旧消息（2 秒宽限期）
- 空 msgID 永不被视为重复

### 6.11 速率限制（core/ratelimit.go）

滑动窗口限制器：每会话维护时间戳列表，窗口外时间戳自动过滤，超过阈值拒绝。默认 20 条/60 秒。后台每 5 分钟清理过期桶。

### 6.12 Markdown 处理（core/markdown.go）

- `StripMarkdown(s)` — 去除格式保留文本
- `MarkdownToTelegramHTML(md)` — 转换为 Telegram 安全 HTML（占位符技术防止标签重叠）
- `SplitMessageCodeFenceAware(text, maxLen)` — 代码围栏感知的消息分片

---

## 七、配置系统

### 7.1 配置加载优先级

1. `-config` 命令行标志
2. `./config.toml`（当前目录）
3. `~/.cc-connect/config.toml`（用户目录）

### 7.2 配置结构

```toml
data_dir = "~/.cc-connect"        # 会话 & 定时任务存储
language = "en"                   # en, zh, zh-TW, ja, es
quiet = false                     # 抑制 thinking/tool 消息
idle_timeout_mins = 120           # Agent 空闲超时（0 = 不超时）

[log]
level = "info"                    # debug, info, warn, error

[[projects]]
name = "my-project"
[projects.agent]
type = "claudecode"               # Agent 类型
[projects.agent.options]
work_dir = "/path/to/project"     # 必需
mode = "default"                  # 权限模式
model = "claude-sonnet-4-20250514"

[[projects.agent.providers]]      # 多 Provider 支持
name = "anthropic"
api_key = "sk-ant-xxx"
[[projects.agent.providers]]
name = "bedrock"
env = {CLAUDE_CODE_USE_BEDROCK = "1"}

[[projects.platforms]]
type = "feishu"
[projects.platforms.options]
app_id = "cli_xxxx"
app_secret = "xxxx"

[speech]                          # 语音转文字
enabled = true
provider = "openai"               # openai, groq, qwen

[tts]                             # 文字转语音
enabled = true
provider = "qwen"                 # openai, qwen
voice = "Cherry"
tts_mode = "voice_only"           # voice_only 或 always

[display]
thinking_max_len = 300            # 推理输出截断
tool_max_len = 500                # 工具输出截断

[stream_preview]
enabled = true
interval_ms = 1500
delta_chars = 30
max_chars = 2000

[rate_limit]
max_messages = 20
window_secs = 60
```

### 7.3 热重载

`/reload` 命令重载：display、providers、commands、aliases，无需重启。

配置持久化使用原子写入（临时文件 + 重命名）+ 互斥锁保护读-改-写周期。

---

## 八、CLI 子命令

```bash
cc-connect                              # 使用默认配置运行
cc-connect --config PATH                # 指定配置文件
cc-connect --version                    # 显示版本

cc-connect daemon install               # 安装为系统服务
cc-connect daemon uninstall             # 卸载服务
cc-connect daemon start/stop/restart    # 管理服务
cc-connect daemon status                # 查看状态
cc-connect daemon logs -f               # 跟踪日志

cc-connect send -m "text"               # 通过 IPC 发送消息
cc-connect send --stdin                 # 从标准输入读取

cc-connect cron add --cron "0 9 * * 1" --prompt "task" --desc "label"
cc-connect cron list [-p project]
cc-connect cron del <job-id>

cc-connect provider add --project X --name Y --api-key Z
cc-connect provider list
cc-connect provider remove --project X --name Y
cc-connect provider import [--db-path PATH]  # 从 cc-switch 导入

cc-connect relay send --to <project> "<message>"

cc-connect config-example               # 打印配置模板
cc-connect update [--pre]               # 自更新
cc-connect check-update                 # 检查更新
```

### 启动序列

1. 子命令路由（非 daemon 子命令在主流程前处理）
2. 日志初始化（daemon 模式写入轮转文件）
3. 配置加载与验证
4. 逐项目初始化：创建 Agent → 注入 Provider → 创建 Platform → 创建 Engine
5. 配置注入：命令、别名、禁词、Display、StreamPreview、RateLimit、语言、空闲超时、Quiet、STT/TTS
6. Cron 调度器启动
7. Unix Socket API 启动
8. 信号监听（SIGINT/SIGTERM → 优雅关闭，/restart → 进程自重启）

---

## 九、Daemon 系统服务

| 平台 | 实现 | 服务文件位置 |
|------|------|-------------|
| Linux (root) | systemd | `/etc/systemd/system/cc-connect.service` |
| Linux (用户) | systemd --user | `~/.config/systemd/user/cc-connect.service` |
| macOS | launchd | `~/Library/LaunchAgents/com.cc-connect.service.plist` |
| Windows | 不支持 | 建议使用 nssm / pm2 |

**元数据持久化：** `~/.cc-connect/daemon.json` 保存日志路径、工作目录、二进制路径、安装时间。

**日志轮转：** `RotatingWriter` 线程安全 io.Writer，超过 maxSize 时轮转，保留 1 个备份。

---

## 十、依赖列表

| 依赖 | 用途 |
|------|------|
| `github.com/BurntSushi/toml` | TOML 配置解析 |
| `github.com/larksuite/oapi-sdk-go/v3` | 飞书 SDK |
| `github.com/slack-go/slack` | Slack SDK |
| `github.com/bwmarrin/discordgo` | Discord SDK |
| `github.com/go-telegram-bot-api/telegram-bot-api/v5` | Telegram SDK |
| `github.com/line/line-bot-sdk-go/v8` | LINE SDK |
| `github.com/open-dingtalk/dingtalk-stream-sdk-go` | 钉钉 SDK |
| `github.com/robfig/cron/v3` | Cron 调度 |
| `github.com/creack/pty` | 伪终端（iFlow Agent） |
| `github.com/gorilla/websocket` | WebSocket（QQ/QQBot） |

日志使用标准库 `log/slog`（Go 1.21+），无外部日志框架。

---

## 十一、环境变量

**Engine 设置：**
- `CC_PROJECT` — 当前项目名
- `CC_SESSION_KEY` — 当前会话键（用于 cron/relay）

**Daemon 模式：**
- `CC_LOG_FILE` — 日志文件路径
- `CC_LOG_MAX_SIZE` — 日志轮转大小（字节）

**Agent 专用：**
| Agent | 变量 |
|-------|------|
| Claude Code | `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN` |
| Codex | `OPENAI_API_KEY`, `OPENAI_BASE_URL` |
| Gemini | `GEMINI_API_KEY`, `GOOGLE_API_KEY` |
| Cursor | `CURSOR_API_KEY` |
| iFlow | `IFLOW_API_KEY`, `IFLOW_BASE_URL` |
| OpenCode | `ANTHROPIC_API_KEY` |

---

## 十二、测试模式

- Stub 实现：`stubAgent`, `stubAgentSession`, `stubPlatformEngine`
- 表驱动测试：命令解析、禁词检测、别名解析、事件处理
- 标准 `testing` 包，无外部测试框架
- 测试文件分布在 `core/` 和 `daemon/` 中

---

## 十三、关键设计模式总结

1. **插件注册模式** — init() 自注册，零配置扩展
2. **接口隔离** — 可选接口不强制所有实现者
3. **会话锁模式** — TryLock/Unlock 防止并发处理
4. **事件流驱动** — Agent 发射事件，Engine 异步构建回复
5. **原子持久化** — 临时文件 + 重命名防止数据损坏
6. **优雅降级** — 平台启动失败不影响其他平台
7. **占位符技术** — Markdown 转换器防止标签重叠
8. **中间件链** — 别名解析 → 速率限制 → 禁词检查 → 命令路由
9. **懒加载缓存** — Skill 和命令首次访问时扫描
10. **慢操作检测** — 超过阈值的操作产生 slog.Warn 日志
