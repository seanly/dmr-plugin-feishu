# Feishu 群聊支持实现方案

> **版本**: v2.0  
> **日期**: 2026-04-05  
> **目标**: 支持群聊@触发、话题隔离、独立审批

---

## 核心需求

| # | 需求 | 说明 |
|---|------|------|
| 1 | **@触发** | 群聊中必须@机器人才响应 |
| 2 | **话题隔离** | 每个话题(thread)独立上下文 |
| 3 | **独立审批** | 审批发送到管理员私聊，不在群里 |
| 4 | **管理员审批** | 只有配置的管理员能审批，不是群成员 |

---

## Tape 命名规范

```
私聊:           feishu:p2p:<chat_id>
群聊话题:       feishu:group:<chat_id>:thread:<thread_id>
群聊无话题:     feishu:group:<chat_id>:main
```

**关键点**:
- 群聊和私聊完全隔离，不会冲突
- 每个话题独立 tape，独立上下文
- 群聊主会话使用 `:main` 后缀，与话题区分

---

## 配置设计

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "~/.dmr/plugins/dmr-plugin-feishu"

[plugins.config]

# === 群聊总开关 ===
group_enabled = true  # true=启用群聊功能, false=仅支持私聊

# === Bot 配置 ===
[[plugins.config.bots]]
app_id = "cli_xxx"
app_secret = "xxx"
verification_token = "xxx"
encrypt_key = ""
allow_from = ["ou_xxx", "ou_yyy"]  # 允许使用的用户 open_id

# === 群聊管理员配置 ===
# 当群聊中触发需要审批的操作时，审批请求会发给这些管理员的私聊
# 注意：管理员的 open_id 必须在 allow_from 列表中

[[plugins.config.admins]]
open_id = "ou_xxx"  # 管理员 1 的 open_id
name = "张三"        # 显示名称（可选，用于日志）

[[plugins.config.admins]]
open_id = "ou_yyy"  # 管理员 2 的 open_id
name = "李四"
```

**配置说明**：

| 配置项 | 说明 |
|--------|------|
| `group_enabled` | **群聊总开关**。`true` 时启用群聊功能（可接收@消息）；`false` 时仅支持私聊，群聊消息被忽略。 |
| `allow_from` | 允许使用机器人的用户 open_id 列表。私聊时只有这些用户能触发；群聊时所有群成员都能@机器人，但审批会发给 `admins` 中配置的管理员。 |
| `admins` | 群聊审批的管理员列表。当群聊中触发需要审批的操作时，审批请求会发给这些管理员的私聊窗口。 |

---

## 消息处理流程

### 1. 消息接收

```go
func (p *Plugin) handleMessageReceive(ctx context.Context, botInst *bot.Instance, event *larkim.P2MessageReceiveV1) error {
    chatType := getChatType(event)  // "p2p" or "group"
    
    // === 私聊 ===
    if chatType == "p2p" {
        return p.handleP2PMessage(ctx, botInst, event)
    }
    
    // === 群聊 ===
    if chatType == "group" {
        return p.handleGroupMessage(ctx, botInst, event)
    }
    
    return nil
}
```

### 2. 群聊处理

```go
func (p *Plugin) handleGroupMessage(ctx context.Context, botInst *bot.Instance, event *larkim.P2MessageReceiveV1) error {
    // 0. 检查群聊总开关
    if !p.config.GroupEnabled {
        log.Printf("feishu: group message ignored (group_enabled=false)")
        return nil
    }
    
    // 1. 检查是否@了机器人（必须@才响应，写死逻辑）
    mentionType := p.checkMention(event, botInst)
    if mentionType == MentionTypeNone {
        log.Printf("feishu: group message ignored (bot not mentioned)")
        return nil
    }
    
    // 2. 不响应 @所有人（写死逻辑）
    if mentionType == MentionTypeAtAll {
        log.Printf("feishu: group message ignored (at all)")
        return nil
    }
    
    // 3. 获取话题ID
    chatID := getChatID(event)
    threadID := getThreadID(event)  // 可能为空
    senderID := getSenderID(event)
    
    // 3. 生成 tape
    var tape string
    if threadID != "" {
        // 话题模式
        tape = fmt.Sprintf("feishu:group:%s:thread:%s", chatID, threadID)
    } else {
        // 主会话模式
        tape = fmt.Sprintf("feishu:group:%s:main", chatID)
    }
    
    // 4. 提取内容（去掉@机器人的部分）
    content := p.extractContentWithoutMention(event)
    
    // 5. 创建任务
    job := &Job{
        TapeName:         tape,
        ChatID:           chatID,
        ThreadID:         threadID,      // 用于回复
        SenderID:         senderID,      // 记录谁触发的
        Bot:              botInst,
        Content:          content,
        IsGroup:          true,
        TriggerMessageID: getMessageID(event),
    }
    
    // 6. 入队处理
    p.queues.Enqueue(job)
    return nil
}
```

### 3. @检测（写死逻辑）

```go
type MentionType int

const (
    MentionTypeNone   MentionType = iota // 无@
    MentionTypeBot                       // @机器人
    MentionTypeAtAll                     // @所有人
)

// checkMention 检测@类型
// 返回值：
//   - MentionTypeNone: 没有@机器人，忽略
//   - MentionTypeBot: @了机器人，继续处理
//   - MentionTypeAtAll: 只@了所有人，忽略
func (p *Plugin) checkMention(event *larkim.P2MessageReceiveV1, botInst *bot.Instance) MentionType {
    message := event.Event.Message
    
    // 检查 mentions 数组
    if message.Mentions != nil && len(message.Mentions) > 0 {
        for _, mention := range message.Mentions {
            if mention.Id != nil && mention.Id.OpenId != nil {
                openID := *mention.Id.OpenId
                
                // 检查是否是 @所有人
                if openID == "all" || mention.Tag != nil && *mention.Tag == "all" {
                    return MentionTypeAtAll
                }
                
                // 检查是否是 @机器人
                if openID == botInst.BotOpenID {
                    return MentionTypeBot
                }
            }
        }
    }
    
    return MentionTypeNone
}

// 提取去掉@后的纯内容
func (p *Plugin) extractContentWithoutMention(event *larkim.P2MessageReceiveV1) string {
    content := extractContent(event)
    
    // 去掉 @机器人的文本
    // 例如: "@机器人 你好" -> "你好"
    // 实现方式取决于消息类型（text/post）
    
    return strings.TrimSpace(content)
}
```

> **注意**：以上逻辑写死，不提供配置开关：
> - 必须 **@机器人** 才响应
> - **不响应 @所有人**

---

## 回复逻辑

```go
func (p *Plugin) ReplyAgentOutput(ctx context.Context, job *Job, output string) error {
    if !job.IsGroup {
        // 私聊：使用现有逻辑
        return p.replyP2P(ctx, job, output)
    }
    
    // === 群聊回复 ===
    if job.ThreadID != "" {
        // 话题内：回复到话题
        return job.Bot.Client.ReplyInThread(ctx, job.ChatID, job.ThreadID, output)
    } else {
        // 主会话：回复到主聊天
        return job.Bot.Client.SendToGroupChat(ctx, job.ChatID, output)
    }
}
```

---

## 审批流程（关键）

### 审批路由

```go
func (p *Plugin) RequestApproval(req *proto.ApprovalRequest, resp *proto.ApprovalResult) error {
    tape := req.Tape
    
    // 判断是群聊还是私聊
    if strings.HasPrefix(tape, "feishu:group:") {
        // === 群聊审批：发给管理员 ===
        return p.handleGroupApproval(req, resp)
    } else {
        // === 私聊审批：发给触发用户 ===
        return p.handleP2PApproval(req, resp)
    }
}

func (p *Plugin) handleGroupApproval(req *proto.ApprovalRequest, resp *proto.ApprovalResult) error {
    // 1. 获取所有管理员
    admins := p.cfg.Admins
    if len(admins) == 0 {
        resp.Choice = bot.ChoiceDenied
        resp.Comment = "no admin configured"
        return nil
    }
    
    // 2. 构建审批消息（包含群聊上下文）
    approvalMsg := p.buildGroupApprovalMessage(req)
    
    // 3. 发送给所有管理员（私聊）
    for _, admin := range admins {
        err := p.sendApprovalToAdmin(admin.OpenID, approvalMsg)
        if err != nil {
            log.Printf("feishu: failed to send approval to admin %s: %v", admin.Name, err)
        }
    }
    
    // 4. 等待任意管理员回复
    // 第一个回复的管理员决定结果
    reply := p.waitForAdminReply(req.Tape, len(admins))
    resp.Choice = reply.Choice
    resp.Comment = reply.Comment
    
    return nil
}

func (p *Plugin) buildGroupApprovalMessage(req *proto.ApprovalRequest) string {
    // 解析 tape 获取信息
    groupName := p.getGroupName(req.Tape)  // 获取群名称
    senderName := p.getSenderName(req.Tape) // 获取触发用户名称
    
    var b strings.Builder
    b.WriteString("## 🔴 DMR 群聊操作需要审批\n\n")
    b.WriteString(fmt.Sprintf("**群聊**: %s\n", groupName))
    b.WriteString(fmt.Sprintf("**用户**: %s\n", senderName))
    b.WriteString(fmt.Sprintf("**工具**: `%s`\n\n", req.Tool))
    
    // ... 工具参数详情 ...
    
    b.WriteString("\n**回复**: y/s/a/n")
    return b.String()
}
```

### 管理员回复处理

```go
func (p *Plugin) TryResolveApproval(chatID, content string) bool {
    // 检查是否是管理员的私聊回复
    
    // 1. 验证发送者是管理员
    senderID := getSenderIDFromContext()
    if !p.isAdmin(senderID) {
        return false  // 不是管理员，忽略
    }
    
    // 2. 查找待审批的请求
    // 需要维护一个 pendingApprovals map
    // key: tape, value: approvalWait
    
    // 3. 解析回复
    choice, comment := parseApprovalChoice(content)
    
    // 4. 通知等待的审批
    // ...
    
    return true
}
```

---

## 数据结构

### Job 结构

```go
type Job struct {
    // 通用字段
    TapeName         string
    ChatID           string
    Bot              *bot.Instance
    SenderID         string
    Content          string
    TriggerMessageID string
    
    // 群聊相关
    IsGroup          bool          // 是否是群聊
    ThreadID         string        // 话题ID（群聊可能为空）
    GroupChatID      string        // 群聊ID（与ChatID相同，但语义更清晰）
}
```

### 配置结构

```go
// 群聊行为写死，不提供配置：
// - 必须 @机器人 才响应
// - 不响应 @所有人

type AdminConfig struct {
    OpenID string  // 管理员 open_id
    Name   string  // 显示名称
}
```

---

## 队列策略（重要）

### 默认策略：Per-Tape 并行

```
workers map[string]chan *Job  // tapeName -> job channel
```

| 场景 | 队列 Key | 执行方式 |
|------|---------|---------|
| 私聊用户 A | `feishu:p2p:<chat_a>` | 串行（同一用户） |
| 私聊用户 B | `feishu:p2p:<chat_b>` | 串行（同一用户） |
| 群聊话题 1 | `feishu:group:<gid>:thread:<tid1>` | 串行（同一话题） |
| 群聊话题 2 | `feishu:group:<gid>:thread:<tid2>` | 串行（同一话题） |
| **不同话题之间** | - | **并行执行** |

### 群聊多用户@场景

**问题**：群里 5 个用户同时 @机器人，如何处理？

```
时间线:
├─ 用户 A @机器人（话题 Thread-1）→ 加入队列 Thread-1，启动处理
├─ 用户 B @机器人（话题 Thread-2）→ 加入队列 Thread-2，启动处理（并行）
├─ 用户 C @机器人（话题 Thread-3）→ 加入队列 Thread-3，启动处理（并行）
└─ 用户 D @机器人（无话题）      → 加入队列 main，启动处理（并行）

结果：4 个 RunAgent 并行执行
```

**影响**：
- ✅ **不同话题/用户**：并行处理，互不等待
- ⚠️ **资源消耗**：多个 RunAgent 同时运行，消耗更多资源
- ⚠️ **并发限制**：大量并行可能导致 DMR 主机压力过大

### 可选策略：Per-Chat 串行

如果希望群里所有消息串行处理（降低并发压力）：

```go
// 修改 tape 生成逻辑，群聊统一使用 chat_id 作为 queue key
if chatType == "group" {
    // 所有群聊消息使用同一个队列（串行）
    queueKey = fmt.Sprintf("feishu:group:%s", chatID)
    tape = fmt.Sprintf("feishu:group:%s:thread:%s", chatID, threadID)  // tape 仍区分话题
}
```

**效果**：
- 群里所有消息（无论哪个用户、哪个话题）串行处理
- 降低并发压力，但响应变慢

**默认推荐**：Per-Tape 并行（当前设计），更符合多话题独立上下文的语义。

---

## 实现步骤

### 阶段 1：基础框架 ✅
1. ✅ 添加 `GroupConfig` 和 `AdminConfig` 到配置 (`internal/plugin/config.go`)
2. ✅ 修改 `handleMessageReceive` 支持群聊分支 (`internal/inbound/receiver.go`)
3. ✅ 实现 `CheckMention` 检测 (`internal/inbound/group.go`)
4. ✅ 测试 @触发

### 阶段 2：话题支持 ✅
5. ✅ 实现话题 tape 命名 (`internal/inbound/group.go`)
6. ✅ 修改回复逻辑，支持话题内回复 (`internal/bot/message.go`)
7. ✅ 测试话题隔离

### 阶段 3：审批路由 ✅
8. ✅ 实现管理员配置 (`internal/plugin/config.go`)
9. ✅ 修改审批逻辑，群聊发给管理员 (`internal/plugin/plugin.go`)
10. ✅ 实现管理员回复处理 (`internal/plugin/plugin.go`)
11. ✅ 测试审批流程

### 阶段 4：优化（可选）
12. ⬜ 支持 @所有人 配置（当前硬编码忽略）
13. ⬜ 审批消息添加上下文（群名、用户名）
14. ⬜ 多管理员支持（任一管理员可审批）

---

## 关键问题解答

### Q1: 如何获取 bot 的 open_id？
配置方式：在 `[[plugins.config.bots]]` 中添加 `bot_open_id = "ou_xxx"`

获取方法：
1. 在 Feishu 开发者后台查看机器人信息
2. 或通过 API 调用获取：`GET /open-apis/bot/v3/info`
3. 或在私聊中发送 `,id` 命令，查看返回的 Open ID

```toml
[[plugins.config.bots]]
app_id = "cli_xxx"
app_secret = "xxx"
bot_open_id = "ou_xxx"  # 用于群聊 @mention 检测
```

### Q: 如何获取群名称？
```go
// 通过 Feishu API
// GET /open-apis/im/v1/chats/{chat_id}
// 但需要在群里才能获取
// 或者首次收到消息时缓存群信息
```

### Q: 管理员如何知道是哪个群的审批？
```go
// 审批消息中包含群名称
approvalMsg := fmt.Sprintf(
    "群聊: %s\n用户: %s\n操作: %s",
    groupName, senderName, toolName,
)
```

### Q: 多管理员如何协调？
```go
// 发送给所有管理员，第一个回复的决定结果
// 其他管理员的后续回复被忽略
// 可以记录谁审批的，用于审计
```

### Q: 能否关闭 @检测，让机器人响应所有群聊消息？
```
不能。必须 @机器人 才响应，这是写死的逻辑。
如果需要，可以提 feature request，但不建议（会导致机器人太吵）。
```

---

## 风险评估

| 风险 | 级别 | 缓解措施 |
|------|------|---------|
| 消息量大导致 @检测性能问题 | 🟡 中 | 缓存 bot open_id，避免重复计算 |
| 群聊审批无人响应 | 🟡 中 | 配置多个管理员，设置审批超时 |
| 管理员误判群聊来源 | 🟢 低 | 审批消息清晰显示群名、用户名 |
| 话题 tape 无限增长 | 🟢 低 | 飞书话题本身有限制，通常几百个 |

## 设计原则

**写死的行为**（不提供配置开关）：
1. 群聊必须 **@机器人** 才响应
2. **不响应 @所有人**
3. 私聊任意消息都响应

这样设计的理由：
- 避免机器人在群里太吵
- 减少用户配置复杂度
- 符合大多数使用场景

---

## 与旧版对比

| 特性 | 旧版 (P2P only) | 新版 (P2P + Group) |
|------|-----------------|-------------------|
| 支持场景 | 仅私聊 | 私聊 + 群聊 |
| 触发方式 | 任意消息 | 私聊任意，群聊 **必须@** |
| 审批接收 | 触发用户 | 私聊→用户，群聊→管理员 |
| 上下文 | chat 级别 | 话题(thread)级别 |
| 配置复杂度 | 简单 | 增加管理员配置 |

---

## 实现状态总结

### 已实现功能 ✅

| 功能 | 文件 | 说明 |
|------|------|------|
| `group_enabled` 开关 | `internal/plugin/config.go` | 默认关闭，需显式开启 |
| `admins` 配置 | `internal/plugin/config.go` | 支持多管理员 |
| `bot_open_id` 配置 | `internal/plugin/config.go` | 用于 @mention 检测 |
| @mention 检测 | `internal/inbound/group.go` | 硬编码必须@才响应 |
| @all 忽略 | `internal/inbound/group.go` | 硬编码不响应 @所有人 |
| 话题 tape 命名 | `internal/inbound/group.go` | `feishu:group:<id>:thread:<tid>` |
| 群聊消息处理 | `internal/inbound/receiver.go` | 分支处理 p2p/group |
| 审批路由 | `internal/plugin/plugin.go` | 群聊→管理员 P2P |
| 工具支持 | `internal/tools/` | sendText/sendFile 支持群聊 |

### 核心设计原则

1. **隔离**：私聊和群聊完全隔离，不会冲突
2. **话题**：每个话题独立 tape，独立上下文
3. **审批分离**：群聊审批在管理员私聊，不在群里讨论
4. **管理员控制**：只有配置的管理员能审批群聊操作
5. **显式开启**：群聊功能默认关闭，需 `group_enabled = true`

### 配置示例

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "~/.dmr/plugins/dmr-plugin-feishu"

[plugins.config]
group_enabled = true  # 开启群聊支持

[[plugins.config.bots]]
app_id = "cli_xxx"
app_secret = "xxx"
bot_open_id = "ou_botxxx"  # 机器人的 open_id

[[plugins.config.admins]]
open_id = "ou_admin1"  # 管理员 1
name = "张三"

[[plugins.config.admins]]
open_id = "ou_admin2"  # 管理员 2
name = "李四"
```
