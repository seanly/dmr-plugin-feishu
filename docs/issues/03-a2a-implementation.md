# 飞书 A2A 实现方案

## 概述

本文档描述在飞书插件中实现 A2A (Agent-to-Agent) 协议的具体方案。

**前提条件**: 已申请并获得飞书 "获取群聊中所有消息" 权限。

## 架构设计

### 整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                     dmr-plugin-feishu                           │
│                                                                  │
│  ┌─────────────────────────────────────────────────────────┐    │
│  │                   Bot Registry                           │    │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐              │    │
│  │  │ 开发助手  │  │ 安全助手  │  │ 测试助手  │              │    │
│  │  │(app_id_1)│  │(app_id_2)│  │(app_id_3)│              │    │
│  │  └────┬─────┘  └────┬─────┘  └────┬─────┘              │    │
│  │       │             │             │                     │    │
│  │       └─────────────┴─────────────┘                     │    │
│  │                     │                                   │    │
│  │              Message Router                             │    │
│  └─────────────────────┬───────────────────────────────────┘    │
│                        │                                         │
│  ┌─────────────────────▼───────────────────────────────────┐    │
│  │                   A2A Engine                             │    │
│  │  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐      │    │
│  │  │ Protocol    │  │ Session     │  │ Deduplicator│      │    │
│  │  │ Handler     │  │ Manager     │  │             │      │    │
│  │  └─────────────┘  └─────────────┘  └─────────────┘      │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
                              │
                              │ 飞书开放 API
                              ▼
                    ┌─────────────────┐
                    │   飞书服务器     │
                    └─────────────────┘
```

### 组件说明

| 组件 | 职责 |
|------|------|
| Bot Registry | 管理多个飞书 Bot，维护 app_id → Bot 映射 |
| Message Router | 区分用户消息和 A2A 消息，路由到不同处理器 |
| A2A Engine | A2A 协议实现，包括消息编解码、会话管理、去重 |

## 配置设计

### 多 Bot 配置

```toml
[[plugins]]
name = "feishu"
enabled = true

# ========== Bot 列表 ==========

[[plugins.config.bots]]
id = "developer"
app_id = "cli_xxx"
app_secret = "secret:feishu_dev_secret"
# 能力标签，用于任务路由
capabilities = ["coding", "golang", "nodejs"]
# 允许响应的群聊
allowed_group_chats = ["oc_xxx", "oc_yyy"]

[[plugins.config.bots]]
id = "security"
app_id = "cli_yyy"
app_secret = "secret:feishu_sec_secret"
capabilities = ["security", "review", "audit"]
allowed_group_chats = ["oc_xxx"]

[[plugins.config.bots]]
id = "tester"
app_id = "cli_zzz"
app_secret = "secret:feishu_test_secret"
capabilities = ["testing", "qa", "automation"]
allowed_group_chats = ["oc_xxx"]

# ========== A2A 配置 ==========

[plugins.config.a2a]
enabled = true

# 启用 A2A 的群聊
collaboration_group_chats = ["oc_xxx"]

# 消息格式: "text" | "card" | "mixed"
message_format = "text"

# 防循环配置
max_hops = 5

# 去重 TTL（秒）
dedup_ttl = 300

# 任务默认超时（秒）
default_task_timeout = 300
```

## 消息格式设计

### 方案对比

飞书消息格式限制下，A2A 消息有三种编码方案：

#### 方案 A：纯文本编码（推荐）

```go
// A2A 消息格式
"🔒A2A|v1|{type}|{base64_payload}|{signature}"

// 示例
"🔒A2A|v1|task_request|eyJ0YXNrX2lkIjoiLi4uIn0=|sha256:abc123"
```

**优点**:
- 兼容所有飞书消息类型（文本、群聊、私聊）
- WebSocket/Webhook 都能正常接收
- 实现简单

**缺点**:
- 不够优雅
- 用户可见编码内容（除非隐藏）

#### 方案 B：卡片消息（用户体验好）

```json
{
  "msg_type": "interactive",
  "card": {
    "config": {
      "wide_screen_mode": true
    },
    "header": {
      "title": {
        "tag": "plain_text",
        "content": "🔒 A2A Task Request"
      },
      "template": "blue"
    },
    "elements": [
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "**From:** @developer\n**Type:** Task Request\n**Task:** Code Review"
        }
      },
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "```json\n{\"task_id\":\"xxx\",\"payload\":\"...\"}\n```"
        }
      },
      {
        "tag": "hr"
      },
      {
        "tag": "note",
        "elements": [
          {
            "tag": "plain_text",
            "content": "💡 此消息为 Agent 间协作消息"
          }
        ]
      }
    ]
  }
}
```

**优点**:
- 用户体验好，结构化展示
- 可以折叠/隐藏技术细节

**缺点**:
- 卡片长度限制（约 30KB）
- 大 payload 需要分片
- 解析复杂度稍高

#### 方案 C：混合方案（推荐用于生产）

```go
// 小型消息：直接文本编码
"🔒A2A|v1|status_update|eyJzdGF0dXMiOiJkb2luZyJ9|sig"

// 大型消息：卡片 + 元数据
{
  "msg_type": "interactive",
  "card": {
    "header": {
      "title": "🔒 A2A Task Result"
    },
    "elements": [
      {
        "tag": "div",
        "text": {
          "tag": "lark_md",
          "content": "任务结果已生成，点击查看详情"
        }
      },
      {
        "tag": "action",
        "actions": [
          {
            "tag": "button",
            "text": "查看结果",
            "type": "primary",
            "value": {
              "a2a_ref": "task_xxx"
            }
          }
        ]
      }
    ]
  },
  // 元数据用于 A2A 解析
  "extra": "A2A:v1:result:task_xxx:sha256:abc"
}
```

### 消息格式规范

采用方案 A（纯文本）作为基础规范：

```
格式: 🔒A2A|{version}|{type}|{payload_base64}|{signature}

字段:
- 🔒A2A: 固定标记，用于识别 A2A 消息
- version: 协议版本，如 "v1"
- type: 消息类型
- payload_base64: Base64 编码的 JSON payload
- signature: 可选的签名，格式 "sha256:{hash}"

示例:
🔒A2A|v1|task_request|eyJ0YXNrX2lkIjoiMSJ9|sha256:abc123
```

## 核心实现

### 1. A2A 消息结构

```go
package a2a

// Message A2A 消息
type Message struct {
    Version   string    `json:"version"`
    Type      MsgType   `json:"type"`
    MsgID     string    `json:"msg_id"`
    ThreadID  string    `json:"thread_id"`
    FromAgent string    `json:"from_agent"`
    ToAgent   string    `json:"to_agent,omitempty"`
    Timestamp time.Time `json:"timestamp"`
    TTL       int       `json:"ttl"`
    Hops      int       `json:"hops"`
    Payload   Payload   `json:"payload"`
}

type MsgType string

const (
    TypeTaskRequest   MsgType = "task_request"
    TypeTaskResponse  MsgType = "task_response"
    TypeTaskDelegate  MsgType = "task_delegate"
    TypeResultShare   MsgType = "result_share"
    TypeStatusUpdate  MsgType = "status_update"
    TypeCapabilityQuery    MsgType = "cap_query"
    TypeCapabilityResponse MsgType = "cap_response"
    TypeConsensus     MsgType = "consensus"
    TypeError         MsgType = "error"
)

type Payload struct {
    Task   *TaskInfo   `json:"task,omitempty"`
    Result *ResultInfo `json:"result,omitempty"`
    Error  *ErrorInfo  `json:"error,omitempty"`
    // ...
}
```

### 2. 消息编解码

```go
// Encode 将 A2A 消息编码为飞书文本
func Encode(msg *Message) (string, error) {
    payloadJSON, err := json.Marshal(msg.Payload)
    if err != nil {
        return "", err
    }
    
    payloadB64 := base64.StdEncoding.EncodeToString(payloadJSON)
    
    // 简化的签名（可选）
    sig := computeSignature(payloadJSON)
    
    return fmt.Sprintf("🔒A2A|%s|%s|%s|%s",
        msg.Version,
        msg.Type,
        payloadB64,
        sig,
    ), nil
}

// Decode 从飞书文本解码 A2A 消息
func Decode(text string) (*Message, error) {
    // 检查前缀
    if !strings.HasPrefix(text, "🔒A2A|") {
        return nil, ErrNotA2AMessage
    }
    
    parts := strings.Split(text, "|")
    if len(parts) != 5 {
        return nil, ErrInvalidFormat
    }
    
    msg := &Message{
        Version: parts[1],
        Type:    MsgType(parts[2]),
    }
    
    payloadJSON, err := base64.StdEncoding.DecodeString(parts[3])
    if err != nil {
        return nil, err
    }
    
    if err := json.Unmarshal(payloadJSON, &msg.Payload); err != nil {
        return nil, err
    }
    
    // 验证签名（可选）
    // ...
    
    return msg, nil
}

// IsA2AMessage 检查文本是否是 A2A 消息
func IsA2AMessage(text string) bool {
    return strings.HasPrefix(text, "🔒A2A|")
}
```

### 3. 消息处理器

```go
// A2AHandler A2A 消息处理器
type A2AHandler struct {
    registry    *bot.Registry
    dedup       *Deduplicator
    collabMgr   *CollaborationManager
    config      *A2AConfig
}

func (h *A2AHandler) Handle(bot *bot.Bot, event *larkim.Event) error {
    content := event.Content
    
    // 解码 A2A 消息
    msg, err := a2a.Decode(content)
    if err != nil {
        return err
    }
    
    // 基础验证
    if err := h.validate(msg); err != nil {
        return err
    }
    
    // 去重检查
    if h.dedup.IsDuplicate(msg.MsgID) {
        return nil
    }
    
    // 目标检查
    if msg.ToAgent != "" && msg.ToAgent != bot.ID {
        return nil // 不是发给我的
    }
    
    // 分发处理
    switch msg.Type {
    case a2a.TypeTaskRequest:
        return h.handleTaskRequest(bot, msg, event)
    case a2a.TypeTaskResponse:
        return h.handleTaskResponse(bot, msg, event)
    case a2a.TypeResultShare:
        return h.handleResultShare(bot, msg, event)
    // ...
    default:
        return fmt.Errorf("unknown message type: %s", msg.Type)
    }
}

func (h *A2AHandler) validate(msg *a2a.Message) error {
    // 版本检查
    if msg.Version != "v1" {
        return fmt.Errorf("unsupported version: %s", msg.Version)
    }
    
    // TTL 检查
    if time.Since(msg.Timestamp) > time.Duration(msg.TTL)*time.Second {
        return fmt.Errorf("message expired")
    }
    
    // 跳数检查
    if msg.Hops >= h.config.MaxHops {
        return fmt.Errorf("max hops exceeded")
    }
    
    return nil
}
```

### 4. 发送 A2A 消息

```go
// SendA2AMessage 发送 A2A 消息
func (b *Bot) SendA2AMessage(toBotID string, msg *a2a.Message, chatID string) error {
    // 获取目标 Bot 的 OpenID
    toBot := b.registry.Get(toBotID)
    if toBot == nil {
        return fmt.Errorf("target bot not found: %s", toBotID)
    }
    
    // 编码消息
    text, err := a2a.Encode(msg)
    if err != nil {
        return err
    }
    
    // 飞书要求：必须@目标 Bot，否则对方收不到（取决于权限配置）
    content := fmt.Sprintf("<at user_id=\"%s\"></at> %s", toBot.OpenID, text)
    
    // 发送
    req := larkim.NewCreateMessageReqBuilder().
        ReceiveIdType(larkim.ReceiveIdTypeChatId).
        Body(larkim.NewCreateMessageReqBodyBuilder().
            ReceiveId(chatID).
            MsgType(larkim.MsgTypeText).
            Content(content).
            Build()).
        Build()
    
    _, err = b.Client.Im.Message.Create(context.Background(), req)
    return err
}
```

### 5. 协作会话管理

```go
// CollaborationManager 管理 A2A 协作会话
type CollaborationManager struct {
    sessions map[string]*Session // thread_id -> Session
    mu       sync.RWMutex
}

type Session struct {
    ID           string
    ThreadID     string
    ChatID       string
    Initiator    string    // 发起者 Bot ID
    Participants []string  // 参与的所有 Bot
    Tasks        map[string]*Task
    Status       SessionStatus
    CreatedAt    time.Time
}

func (cm *CollaborationManager) StartSession(initiator *bot.Bot, event *larkim.Event) (*Session, error) {
    threadID := event.Message.ThreadId
    if threadID == nil || *threadID == "" {
        // 飞书消息可能没有 thread_id，使用首条消息 ID
        threadID = &event.Message.MessageId
    }
    
    session := &Session{
        ID:           uuid.New().String(),
        ThreadID:     *threadID,
        ChatID:       *event.Message.ChatId,
        Initiator:    initiator.ID,
        Participants: cm.registry.GetBotsInChat(*event.Message.ChatId),
        Tasks:        make(map[string]*Task),
        Status:       SessionActive,
        CreatedAt:    time.Now(),
    }
    
    cm.mu.Lock()
    cm.sessions[session.ThreadID] = session
    cm.mu.Unlock()
    
    return session, nil
}
```

## 集成到飞书插件

### 消息处理流程

```go
// FeishuPlugin.handleMessage 修改
func (p *FeishuPlugin) handleMessage(bot *bot.Bot, event *larkim.Event) {
    content := event.Content
    
    // 1. 检查是否是 A2A 消息
    if a2a.IsA2AMessage(content) {
        if !p.config.A2A.Enabled {
            return
        }
        if err := p.a2aHandler.Handle(bot, event); err != nil {
            log.Printf("A2A handle error: %v", err)
        }
        return
    }
    
    // 2. 检查@提及
    if isMentionToBot(content, bot.OpenID) {
        p.handleUserMessage(bot, event)
        return
    }
    
    // 3. 普通消息（仅当配置为接收所有消息时）
    if p.config.ReceiveAllMessages {
        p.handleUserMessage(bot, event)
    }
}
```

### 新增 A2A 工具

```go
func (p *FeishuPlugin) ProvideTools(req *proto.ProvideToolsRequest, resp *proto.ProvideToolsResponse) error {
    resp.Tools = []proto.ToolDef{
        // ... 原有工具 ...
        
        {
            Name:        "feishuDelegateToAgent",
            Description: "Delegate task to another agent in the group",
            ParametersJSON: `{
                "type": "object",
                "properties": {
                    "agent_name": {"type": "string", "description": "Target agent name or capability"},
                    "task": {"type": "string", "description": "Task description"},
                    "context": {"type": "object", "description": "Additional context"}
                },
                "required": ["task"]
            }`,
        },
        {
            Name:        "feishuListAvailableAgents",
            Description: "List all agents in current chat with their capabilities",
        },
        {
            Name:        "feishuBroadcastToAgents",
            Description: "Broadcast a message to all agents",
            ParametersJSON: `{
                "type": "object",
                "properties": {
                    "message": {"type": "string"},
                    "require_response": {"type": "boolean"}
                }
            }`,
        },
    }
    return nil
}
```

## 安全考虑

### 消息签名

```go
func computeSignature(payload []byte, secret string) string {
    h := hmac.New(sha256.New, []byte(secret))
    h.Write(payload)
    return hex.EncodeToString(h.Sum(nil))
}

func verifySignature(payload []byte, sig string, secret string) bool {
    expected := computeSignature(payload, secret)
    return hmac.Equal([]byte(sig), []byte(expected))
}
```

### Agent 白名单

```go
func (p *FeishuPlugin) validateA2ASender(msg *a2a.Message) error {
    // 检查发送者是否在白名单
    if !p.config.A2A.TrustedAgents[msg.FromAgent] {
        return fmt.Errorf("untrusted agent: %s", msg.FromAgent)
    }
    return nil
}
```

## 相关文档

- [01-a2a-feasibility.md](01-a2a-feasibility.md) - A2A 可行性分析
- [02-a2a-vs-subagent.md](02-a2a-vs-subagent.md) - A2A vs Subagent 对比
