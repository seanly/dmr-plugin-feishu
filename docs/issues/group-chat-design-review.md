# Feishu Group Chat Design Review

> **Review Date**: 2026-04-05  
> **Status**: Issues Identified

## 执行摘要

该设计方案**存在 3 个严重问题**和 **2 个需要注意的风险**，需要重新设计后才能实施。

| 级别 | 数量 | 说明 |
|------|------|------|
| 🔴 **严重** | 3 | 会导致功能错误或数据混乱 |
| 🟡 **中等** | 2 | 影响用户体验或增加复杂度 |
| 🟢 **轻微** | 2 | 建议优化但非阻塞 |

---

## 🔴 严重问题

### 1. Tape 命名冲突（Critical）

**问题**：Group normal message 使用 `feishu:p2p:<sender_id>` 会与真实 P2P 聊天冲突

```
场景：
- 用户 A 同时与 Bot 私聊（P2P）并在群聊中 @Bot
- P2P tape: feishu:p2p:<user_A_open_id>
- Group normal tape: feishu:p2p:<user_A_open_id>  ← 相同！

后果：
1. 群聊消息和私聊消息历史混合在同一条 tape
2. DMR 上下文混淆（群聊上下文混入私聊）
3. 审批路由错误（可能发送到错误的聊天）
```

**建议修复**：
```go
// 方案 A：完全隔离
tape = fmt.Sprintf("feishu:group:%s:sender:%s", chatID, senderID)

// 方案 B：区分命名空间
tape = fmt.Sprintf("feishu:group-sender:%s:%s", chatID, senderID)
```

---

### 2. Admin 识别机制脆弱（Critical）

**问题**：`allow_from[0]` 作为 admin 的设计不可靠

```go
// 当前设计
admin = allow_from[0]  // 第一个元素
```

**风险**：
1. `allow_from` 是数组，顺序不保证稳定
2. 如果 `allow_from` 为空，无法识别 admin
3. 不支持多 admin（业务上可能需要）
4. 添加新用户到 `allow_from` 可能改变 admin

**建议修复**：
```toml
# config.toml
[[plugins]]
name = "feishu"
[plugins.config]
# 明确的 admin 配置
admins = ["ou_xxx", "ou_yyy"]  # 支持多 admin
# 或
admin = "ou_xxx"  # 单 admin，与 allow_from 分离
```

---

### 3. Admin Chat ID 查找不可靠（Critical）

**问题**：依赖 `sender_id -> chat_id` 映射表

```go
// 设计中的代码
adminChatID := a.plugin.getChatIDForSender(adminID)
```

**问题场景**：
1. Admin 从未私聊过 Bot → 返回空 → 审批被拒绝
2. 但此时群聊消息已经触发了 RunAgent，只是审批无法送达
3. 用户体验差：用户不知道 admin 需要先私聊 bot

**建议修复**：
```go
// 使用 open_id 直接发送消息给 admin
// Feishu API 支持直接发给 user_id/open_id，不需要 chat_id
// POST /open-apis/im/v1/messages
// {
//   "receive_id_type": "open_id",
//   "receive_id": "ou_xxx",
//   ...
// }
```

---

## 🟡 中等问题

### 4. Thread Tape 包含 Bot ID（不必要复杂度）

**问题**：`feishu:group:<chat_id>:<bot_id>:thread:<thread_id>`

**疑问**：
1. 为什么需要 `<bot_id>`？
2. 同一个群的多个 bot 应该共享 thread 上下文吗？
3. 如果 bot 被重新安装（bot_id 改变），历史上下文丢失？

**建议**：
```go
// 简化，bot_id 不必要
// 如果确实需要多 bot 隔离，使用不同的插件实例
tape = fmt.Sprintf("feishu:group:%s:thread:%s", chatID, threadID)
```

---

### 5. Mention 检测缺失细节（Medium）

**问题设计**：
```go
func isBotMentioned(content, botOpenID string) bool
```

**缺失考虑**：
1. **Text 消息**：`{"text": "@Bot 你好"}` + mentions 数组
2. **Post 消息**：富文本中的 mention tag
3. **At all** (`@所有人`)：是否响应？当前设计忽略
4. **Multiple mentions**：用户 @Bot 又 @其他人，是否正常处理？

**建议补充**：
```go
type MentionCheckResult struct {
    IsMentioned   bool
    IsAtAll       bool  // @所有人
    MentionType   string // "text" | "post"
}
```

---

## 🟢 轻微问题

### 6. Queue 策略变更影响（Minor）

当前设计：Per-chat_id queue  
建议变更：Per-tape queue

**影响**：
```
当前：同一个 chat_id 的消息串行处理
变更后：同一个 chat_id 的不同 sender（群聊）并行处理

潜在问题：
- 如果两个 sender 同时 @Bot，DMR 会并行执行两个 RunAgent
- 对于有限资源的操作（如文件写入），可能产生冲突
```

**建议**：保留 per-chat_id queue，群聊内仍串行。

---

### 7. 群聊审批的特殊性（Minor）

**当前设计**：群聊审批发送到 admin P2P

**问题场景**：
```
群聊中有 5 个用户，都 @Bot 触发了需要审批的操作
→ 5 个审批请求都发到 admin 的 P2P
→ admin 看到 5 条消息，但不知道来自哪个群、哪个用户
```

**建议改进**：
```go
// 审批消息中添加上下文
approvalMsg := fmt.Sprintf(
    "## DMR tool approval required\n\n"+
    "- **Tool:** `%s`\n"+
    "- **Source:** Group chat (xxx群)\n"+  // ← 添加来源
    "- **User:** %s\n"+  // ← 添加触发用户
    "- **Risk:** %s\n",
    tool, groupName, userName, risk,
)
```

---

## 设计对比表

| 方面 | 当前设计 | 建议改进 |
|------|---------|---------|
| **Tape 命名** | `feishu:p2p:<sender_id>`（冲突） | `feishu:group:<chat_id>:sender:<sender_id>` |
| **Admin 识别** | `allow_from[0]`（不可靠） | 独立 `admins` 配置项 |
| **Admin 查找** | `sender_id->chat_id` 映射（可能为空） | 直接使用 `open_id` 发送 |
| **Thread Tape** | 包含 `bot_id`（不必要） | 移除 `bot_id` |
| **Queue 策略** | Per-tape（可能并行冲突） | 保留 per-chat_id |
| **审批上下文** | 无群聊信息 | 添加群名、用户名 |

---

## 建议的修改后流程

```
Group message received
    │
    ├─ chat_type = "group"
    │
    ├─ Check mentions[] — is this bot mentioned?
    │   └─ No → Ignore
    │   └─ Yes → Continue
    │
    ├─ Extract sender_id
    │
    ├─ Check thread_id exists?
    │   ├─ No (normal group message):
    │   │   └─ Tape = feishu:group:<chat_id>:sender:<sender_id>
    │   │   └─ Reply to main chat
    │   │
    │   └─ Yes (thread/topic):
    │       └─ Tape = feishu:group:<chat_id>:thread:<thread_id>
    │       └─ Reply in thread
    │
    ├─ Enqueue job (per chat_id queue, not per tape)
    │
    └─ On approval needed:
        └─ Get admins from config
        └─ Send approval to each admin via open_id directly
        └─ Include group name and sender name in approval message
```

---

## 实施建议

### 阶段 1：修复严重问题
1. 修改 tape 命名，避免 P2P 冲突
2. 添加独立的 `admins` 配置项
3. 使用 `open_id` 直接发送审批消息

### 阶段 2：优化体验
4. 完善 mention 检测（支持 post 消息）
5. 添加审批上下文信息
6. 处理 @所有人 场景

### 阶段 3：测试验证
7. P2P 和群聊同时使用的边界测试
8. 多 admin 审批测试
9. 群聊 thread 上下文隔离测试
