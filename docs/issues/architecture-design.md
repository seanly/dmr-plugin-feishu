# Feishu Plugin Architecture Design

> 飞书插件完整架构设计文档
> 
> 涵盖：整体架构、核心组件、群聊设计、配置说明、部署指南

## 目录

- [1. 整体架构](#1-整体架构)
- [2. 核心组件](#2-核心组件)
- [3. 数据流](#3-数据流)
- [4. 群聊架构](#4-群聊架构)
- [5. 配置说明](#5-配置说明)
- [6. 部署指南](#6-部署指南)
- [7. 故障排查](#7-故障排查)

---

## 1. 整体架构

### 1.1 系统架构图

```
┌─────────────────────────────────────────────────────────────────┐
│                    DMR Host (Go)                                │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  External Plugin Manager                                  │  │
│  │  - Load plugin via go-plugin (RPC)                        │  │
│  │  - Hook Registry (ProvideApprover, RegisterTools, etc.)   │  │
│  └───────────────────────────────────────────────────────────┘  │
└───────────────────────┬───────────────────────────────────────┘
                        │ RPC (net/rpc over stdio)
┌───────────────────────▼───────────────────────────────────────┐
│                Feishu Plugin Process (Go)                     │
│                                                               │
│  ┌─────────────┐  ┌──────────────┐  ┌─────────────────────┐  │
│  │   Plugin    │  │    Queue     │  │       Inbound       │  │
│  │   Core      │──│   Manager    │──│     Receiver        │  │
│  │             │  │              │  │                     │  │
│  │ • Lifecycle │  │ • Per-chat   │  │ • WebSocket events  │  │
│  │ • Config    │  │   queue      │  │ • Message parsing   │  │
│  │ • Routing   │  │ • Worker     │  │ • Allowlist check   │  │
│  └──────┬──────┘  └──────────────┘  └─────────────────────┘  │
│         │                                                     │
│  ┌──────▼──────┐  ┌──────────────┐  ┌─────────────────────┐  │
│  │     Bot     │  │    Tools     │  │     Approver        │  │
│  │   Instance  │  │              │  │                     │  │
│  │             │  │ • SendText   │  │ • Text-based        │  │
│  │ • Client    │  │ • SendFile   │  │ • Single/Batch      │  │
│  │ • Approver  │  │              │  │ • Timeout handling  │  │
│  └──────┬──────┘  └──────────────┘  └─────────────────────┘  │
│         │                                                     │
└─────────┼─────────────────────────────────────────────────────┘
          │ HTTPS/WebSocket
┌─────────▼─────────────────────────────────────────────────────┐
│              Feishu/Lark Open Platform                          │
│         (IM messages, Approvals, File uploads)                  │
└─────────────────────────────────────────────────────────────────┘
```

### 1.2 进程架构

```
┌────────────────────────────────────────┐
│         go-plugin (Hashicorp)          │
│  - Handshake protocol version          │
│  - RPC over stdio                      │
│  - Plugin process isolation            │
└────────────────────────────────────────┘

优势：
1. 插件崩溃不影响 DMR 主进程
2. 独立部署、独立升级
3. 语言无关（可用其他语言实现）
```

### 1.3 并发模型

```
┌─────────────────────────────────────────────────┐
│                    Main Goroutine               │
│              (Plugin Init/Shutdown)               │
└─────────────────────────────────────────────────┘
                        │
    ┌───────────────────┼───────────────────┐
    ▼                   ▼                   ▼
┌────────┐        ┌────────┐          ┌────────┐
│ WS #1  │        │ WS #2  │          │ Queue  │
│ Worker │        │ Worker │          │ Worker │
│(per bot│        │(per bot│          │(per chat)
└────────┘        └────────┘          └────────┘
    │                   │                   │
    ▼                   ▼                   ▼
HandleMessage    HandleMessage       ProcessJob
    │                   │                   │
    └───────────────────┴───────────────────┘
                        │
                        ▼
              ┌─────────────────┐
              │  ActiveJobs Map │
              │  (mutex guarded)│
              └─────────────────┘
```

**同步原语**：
- `sync.RWMutex`：保护 `bots`, `routing`, `activeJobs`
- `sync.Mutex`：Queue Manager 的 workers map
- Channel：Job 队列（缓冲 16）

---

## 2. 核心组件

### 2.1 Plugin Core (`internal/plugin/plugin.go`)

**职责**：插件生命周期管理、配置解析、组件协调

**核心结构**：
```go
type Plugin struct {
    cfg          Config
    botsMu       sync.RWMutex
    bots         []*bot.Instance          // 多 Bot 支持
    routingMu    sync.RWMutex
    routing      map[string]*bot.Instance   // chat_id -> Bot 路由
    activeJobsMu sync.RWMutex
    activeJobs   map[string]*queue.Job      // tape -> Job 映射
    queues       *queue.Manager
}
```

**多 Bot 架构**：
- 支持配置多个飞书机器人实例
- 动态路由：根据 `chat_id` 选择对应的 Bot
- 向后兼容：支持单 Bot 的遗留配置

---

### 2.2 Queue Manager (`internal/queue/manager.go`)

**职责**：消息队列管理、并发控制、Job 生命周期

**设计模式**：**Per-Chat 串行队列**

```go
type Manager struct {
    mu      sync.Mutex
    workers map[string]chan *Job // chat_id -> queue channel
}

func (qm *Manager) runWorkerForChat(chatID string, jobs <-chan *Job) {
    // 每个 chat_id 只有一个 worker goroutine
    // 保证同一对话的消息按顺序处理
}
```

**关键特性**：
- **串行处理**：同一 chat 的消息按顺序处理（避免竞态）
- **延迟清理**：Job 在 `RunAgent` 返回后保留 30 秒，支持异步工具调用
- **空闲超时**：Worker 5 分钟无任务自动退出

---

### 2.3 Inbound Receiver (`internal/inbound/receiver.go`)

**职责**：飞书消息接收、解析、过滤

**处理流程**：
```
WebSocket Event
    ↓
HandleMessageReceive
    ↓
├─→ 解析 chat_id, sender_id, message_id
├─→ 去重检查 (Deduper)
├─→ 消息类型判断 (P2P vs Group)
├─→ Allowlist 检查
├─→ 解析内容（文本、图片、文件）
├─→ 构建用户 Prompt
└─→ Enqueue Job
```

**设计亮点**：
- **接口抽象**：`Receiver.Plugin` 是接口，便于测试
- **命令拦截**：`,id` 等命令直接处理，不进入 Agent
- **回复上下文**：自动获取父消息内容（支持多轮对话）

---

### 2.4 Bot Instance (`internal/bot/`)

**职责**：飞书 API 调用、消息发送、文件上传

**组件结构**：
```go
type Instance struct {
    Config   ClientConfig
    Client   *Client        // HTTP API 客户端
    Approver *Approver      // 审批处理器
    WSClient *larkws.Client // WebSocket 连接
    BotID    string         // 用于 @mention 检测
}
```

**WebSocket 重连机制**（指数退避）：
```go
const (
    wsInitialRetryDelay = 5 * time.Second
    wsMaxRetryDelay     = 5 * time.Minute
    wsRetryMultiplier   = 2.0
)

// 重试间隔: 5s → 10s → 20s → 40s → ... → 5min (max)
```

---

### 2.5 Tools (`internal/tools/`)

| 工具 | 功能 | 上下文要求 |
|------|------|-----------|
| `feishuSendText` | 发送文本/Markdown | Job 上下文或 tape_name |
| `feishuSendFile` | 上传并发送文件 | **必须 Job 上下文** |

**设计决策**：
- `SendFile` 必须 Job 上下文（安全性：防止未授权文件访问）
- `SendText` 支持通过 `tape_name` 指定目标（灵活性）

---

### 2.6 Approver (`internal/bot/approver.go`)

**职责**：基于文本的审批流程

**状态机**：
```
Policy Check -> require_approval
                    ↓
         Send approval prompt to chat
                    ↓
         Wait for user reply (y/s/a/n)
                    ↓
         Resolve approval choice
```

---

## 3. 数据流

### 3.1 入站消息流

```
飞书用户发送消息
    ↓
WebSocket -> Lark SDK -> EventDispatcher
    ↓
inbound.Receiver.HandleMessageReceive
    ↓
解析 -> 去重 -> Allowlist 检查 -> 构建 Prompt
    ↓
queue.Manager.Enqueue
    ↓
queue.Worker (per chat, serial)
    ↓
ProcessJob -> SetActiveJob
    ↓
dmr.Client.RunAgent (RPC to DMR Host)
    ↓
DMR Agent Loop (LLM + Tools)
    ↓
(工具调用) -> CallTool -> GetActiveJobByTape
    ↓
ReplyAgentOutput -> 飞书回复
    ↓
ClearActiveJob (after 30s delay)
```

### 3.2 工具调用流

```
DMR Agent 调用 feishuSendText
    ↓
ExternalPlugin.CallTool (RPC Server)
    ↓
plugin.CallTool (in Feishu plugin)
    ↓
GetActiveJobByTape(sessionTape)
    ↓
[找到 Job] 使用 Job 上下文发送
[未找到]   尝试从 args 解析 tape_name/chat_id
    ↓
bot.Client.SendTextToChat / DeliverIMTextForJob
```

---

## 4. 群聊架构

### 4.1 群聊架构概览

```
┌─────────────────────────────────────────────────────────────────┐
│                    群聊消息处理流程                              │
└─────────────────────────────────────────────────────────────────┘
                            │
                            ▼
              ┌─────────────────────────┐
              │  1. 消息接收 (Receiver)  │
              │     - Check GroupEnabled │
              │     - Check @mention     │
              │     - Filter @all        │
              └───────────┬─────────────┘
                          │
                          ▼
              ┌─────────────────────────┐
              │  2. 内容处理            │
              │     - Strip @mentions   │
              │     - Parse message     │
              │     - Reply context     │
              └───────────┬─────────────┘
                          │
                          ▼
              ┌─────────────────────────┐
              │  3. Tape 命名           │
              │     - Thread-aware      │
              │     - Per-thread queue  │
              └───────────┬─────────────┘
                          │
                          ▼
              ┌─────────────────────────┐
              │  4. Agent 处理          │
              │     - Same as P2P       │
              └───────────┬─────────────┘
                          │
                          ▼
              ┌─────────────────────────┐
              │  5. 回复发送            │
              │     - Same chat/thread  │
              └─────────────────────────┘
```

### 4.2 Mention 检测系统

```go
// MentionType 定义了机器人在群聊中被提及的方式
type MentionType int
const (
    MentionTypeNone MentionType = iota   // 未被提及
    MentionTypeAtBot                      // 被@提及
    MentionTypeAtAll                      // @所有人
)
```

**行为策略**：

| 场景 | 处理 | 原因 |
|------|------|------|
| 未被@ | 忽略 | 避免群聊中每条消息都触发 |
| @机器人 | 处理 | 明确的用户意图 |
| @all | **忽略** | 硬编码避免，防止误触发和刷屏 |

### 4.3 话题 (Thread) 支持

**Tape 命名策略**：
```go
// 非话题消息
feishu:group:<chat_id>:main

// 话题消息  
feishu:group:<chat_id>:thread:<thread_id>
```

**Queue Key 策略**：
```go
// 每个话题有独立的队列，实现话题级别的串行处理
func QueueKeyForGroup(chatID, threadID string) string {
    return TapeNameForGroup(chatID, threadID)
}
```

**优势**：
- 不同话题可以**并行处理**
- 同话题内消息**按顺序处理**
- 上下文隔离（不同话题的历史不混淆）

### 4.4 群聊审批路由

```
┌─────────────────────────────────────────────────────────┐
│                   审批请求 (来自 DMR)                     │
│                   tape=feishu:group:xxx:...             │
└────────────────────┬────────────────────────────────────┘
                     │
                     ▼
        ┌────────────────────────┐
        │  resolveApprovalChatID │
        │  识别为群聊 (isGroup=true) │
        └───────────┬────────────┘
                    │
                    ▼
        ┌────────────────────────┐
        │  检查 Admin 配置        │
        │  cfg.GetAdminOpenIDs()  │
        └───────────┬────────────┘
                    │
        ┌───────────┴───────────┐
        │                       │
        ▼                       ▼
   [有 Admin]              [无 Admin]
        │                       │
        ▼                       ▼
发送到 Admin P2P          直接拒绝
(私聊审批)                "no admins configured"
        │
        ▼
等待 Admin 回复
(y/s/a/n)
```

**设计原因**：
- **安全性**：群聊中@所有人进行审批不合适
- **私密性**：审批内容可能敏感，不应公开
- **责任明确**：指定管理员负责审批决策

### 4.5 群聊 vs 私聊 对比

| 特性 | 私聊 (P2P) | 群聊 (Group) |
|------|-----------|-------------|
| **触发条件** | 任意消息 | 必须@机器人 |
| **@all 处理** | N/A | **忽略**（硬编码） |
| **Tape 格式** | `feishu:p2p:<id>` | `feishu:group:<id>:main/thread:<tid>` |
| **审批方式** | 直接在对话中 | 路由到 Admin 私聊 |
| **队列粒度** | Per-chat | Per-thread（话题隔离） |
| **配置文件** | `allow_from` | `group_enabled` + `admins` |

---

## 5. 配置说明

### 5.1 TOML 配置示例

```toml
# DMR 主配置文件

# 模型配置
[model]
name = "kimi-for-coding"
api_key = "${KIMI_API_KEY}"
base_url = "https://api.moonshot.cn/v1"

# 插件列表
[[plugins]]
name = "fs"
enabled = true

[[plugins]]
name = "shell"
enabled = true

[[plugins]]
name = "command"
enabled = true

# ========== Feishu 插件配置 ==========
[[plugins]]
name = "feishu"
enabled = true
path = "./dmr-plugin-feishu"  # 插件二进制路径

[plugins.config]
# ========== 多 Bot 配置 ==========
# 支持配置多个飞书机器人
[[plugins.config.bots]]
app_id = "cli_xxxxxxxxxxxxxxxx"
app_secret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
verification_token = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
encrypt_key = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
# 允许访问的用户 OpenID 列表（空列表允许所有）
allow_from = ["ou_xxxxxxxxxxxxxxxx", "ou_yyyyyyyyyyyyyyyy"]
# Bot 的 OpenID，用于群聊 @mention 检测
bot_open_id = "ou_bot_xxxxxxxxxxxxxxxx"

# 第二个 Bot 配置（可选）
[[plugins.config.bots]]
app_id = "cli_yyyyyyyyyyyyyyyy"
app_secret = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
verification_token = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
encrypt_key = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
allow_from = []
bot_open_id = "ou_bot_yyyyyyyyyyyyyyyy"

# ========== 群聊配置 ==========
# 启用群聊功能
group_enabled = true

# 群聊管理员（审批时发送给这些管理员）
[[plugins.config.admins]]
open_id = "ou_admin_xxxxxxxxxxxxxxxx"
name = "管理员1"

[[plugins.config.admins]]
open_id = "ou_admin_yyyyyyyyyyyyyyyy"
name = "管理员2"

# ========== 文件上传配置 ==========
# 最大文件大小（支持单位: B, KB, MB, GB）
send_file_max_bytes = "30MB"
# 文件路径限制（可选）
send_file_root = "/home/user/workspace"

# ========== 入站媒体配置 ==========
# 启用入站媒体下载（图片、文件）
inbound_media_enabled = true
# 最大下载大小
inbound_media_max_bytes = "10MB"
# 媒体文件保存目录（相对 workspace）
inbound_media_dir = "feishu-inbound"
# 下载超时（秒）
inbound_media_timeout_sec = 45
# 媒体文件保留天数（0 表示不自动清理）
inbound_media_retention_days = 7

# ========== 回复上下文配置 ==========
# 启用回复上下文（获取父消息内容）
inbound_reply_context_enabled = true
# 获取父消息超时（秒）
inbound_reply_context_timeout_sec = 12
# 父消息最大字符数
inbound_reply_context_max_runes = 8000

# ========== 审批配置 ==========
# 审批超时（秒）
approval_timeout_sec = 300
# 去重 TTL（分钟）
dedup_ttl_minutes = 10

# ========== 额外 Prompt ==========
# 直接配置额外 Prompt
extra_prompt = """
You are a helpful assistant for Feishu users.
Always respond in Chinese unless asked otherwise.
"""
# 或从文件加载（优先级低于 extra_prompt）
# extra_prompt_file = "./feishu-prompt.txt"
```

### 5.2 配置项说明

#### Bot 配置 (`bots`)

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `app_id` | string | ✅ | 飞书应用 ID |
| `app_secret` | string | ✅ | 飞书应用密钥 |
| `verification_token` | string | ✅ | 事件订阅验证 Token |
| `encrypt_key` | string | ✅ | 消息加密密钥 |
| `allow_from` | []string | ❌ | 允许的用户 OpenID 列表 |
| `bot_open_id` | string | ❌ | Bot OpenID（群聊@检测用）|

#### 群聊配置

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `group_enabled` | bool | false | 启用群聊功能 |
| `admins` | []Admin | [] | 群聊管理员列表 |

#### 文件上传配置

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `send_file_max_bytes` | string | "30MB" | 最大文件大小 |
| `send_file_root` | string | "" | 文件路径限制 |

#### 入站媒体配置

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `inbound_media_enabled` | bool | true | 启用媒体下载 |
| `inbound_media_max_bytes` | string | "30MB" | 最大下载大小 |
| `inbound_media_dir` | string | "feishu-inbound" | 保存目录 |
| `inbound_media_timeout_sec` | int | 45 | 下载超时 |
| `inbound_media_retention_days` | int | 0 | 保留天数 |

---

## 6. 部署指南

### 6.1 构建

```bash
# 克隆仓库
git clone https://github.com/your/dmr-plugin-feishu.git
cd dmr-plugin-feishu

# 构建二进制文件
make build
# 或
go build -o dmr-plugin-feishu ./cmd/dmr-plugin-feishu

# 构建多平台版本
make build-all
```

### 6.2 配置

1. 在 DMR 配置文件中添加 Feishu 插件配置（见 5.1）
2. 从飞书开放平台获取应用凭证
3. 配置用户 Allowlist 或保持开放
4. 如需群聊，配置管理员列表

### 6.3 启动

```bash
# DMR 会自动加载插件
dmr run --config config.toml

# 查看插件日志
tail -f ~/.dmr/logs/dmr.log | grep feishu
```

### 6.4 验证

```bash
# 检查插件是否加载
dmr plugins

# 测试私聊消息
# 在飞书中给 Bot 发送消息：hello

# 测试群聊消息（如启用）
# 在群中 @Bot：hello
```

---

## 7. 故障排查

### 7.1 常见问题

| 问题 | 可能原因 | 解决方案 |
|------|---------|---------|
| 消息无响应 | WebSocket 未连接 | 检查日志中的 WebSocket 状态 |
| 无法发送消息 | Job 上下文过期 | 工具调用在 30 秒内完成 |
| 群聊无响应 | @mention 检测失败 | 确认 `bot_open_id` 配置正确 |
| 审批未收到 | 未配置 Admin | 检查 `admins` 配置 |
| 文件发送失败 | 超出大小限制 | 调整 `send_file_max_bytes` |

### 7.2 日志级别

```go
// 关键日志关键字
"feishu: starting bot"              // WebSocket 启动
"feishu: bot #0 websocket stopped"  // WebSocket 断开
"feishu: reconnecting in"           // 正在重连
"feishu: processJob"                // 开始处理 Job
"feishu: found job for parent tape" // Subagent 匹配成功
"feishu: no active job for tape"    // Job 未找到
```

### 7.3 调试技巧

```bash
# 启用详细日志（启动 DMR 前）
export DMR_LOG_LEVEL=debug

# 监控插件进程
watch -n 1 'ps aux | grep dmr-plugin-feishu'

# 检查 WebSocket 连接
lsof -i | grep dmr-plugin

# 查看插件 RPC 调用
# 在 DMR 配置中启用 plugin_debug: true
```

---

## 附录：设计决策记录

### ADR-001: Job 生命周期延迟清理

**背景**：早期版本 Job 在 `RunAgent` 返回后立即清理，导致异步工具调用失败。

**决策**：添加 30 秒延迟清理。

**权衡**：
- ✅ 支持异步/子 Agent 工具调用
- ⚠️ 内存占用略有增加（可接受）

### ADR-002: 群聊 @all 忽略

**背景**：群聊中 @all 可能误触发 Bot。

**决策**：硬编码忽略 @all。

**权衡**：
- ✅ 避免误触发和刷屏
- ⚠️ 用户需要通过 @Bot 明确触发

### ADR-003: 群聊审批路由到 Admin P2P

**背景**：群聊中直接发送审批不合适。

**决策**：审批请求路由到配置的管理员私聊。

**权衡**：
- ✅ 安全、私密
- ✅ 责任明确
- ⚠️ 需要配置管理员

---

*文档版本: 1.0*
*最后更新: 2026-04-05*
