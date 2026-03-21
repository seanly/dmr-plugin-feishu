## Implementation Plan: `dmr-plugin-feishu`

> **与当前代码一致的状态（摘要）**：**仅 p2p 入站**（群聊忽略）；tape 仅 **`feishu:p2p:<chat_id>`**；工具审批仅 **`feishu:p2p:`** 前缀。入站任务进入 **全局单队列** 串行执行；**不再**在插件内调用 `TapeHandoff`，`RunAgent` 的 `HistoryAfterEntryID` 固定为 **0**。下文部分段落描述早期的群聊、`feishu:group:` tape、per-thread 并行队列与 periodic handoff，保留作背景参考。

> 目标（历史）：把飞书消息接入 DMR；**当前代码仅处理单聊 p2p**，群聊已移除。
>
> 原目标曾包括（群聊/单聊）：
> 1. 入站触发 agent，出站把 agent 输出回写到“触发消息所在 thread 或主聊天”
> 2. 上下文隔离：用 DMR tape anchor（支持 periodic 移动以避免 token 爆炸）
> 3. 工具审批：`require_approval`
>    - 群聊：直接 Denied（不展示审批 UI）
>    - 单聊：展示审批提示，用户用 `y/s/a/n` 回填决策
>    - 协议透传：在审批请求里带 `ApprovalRequest.Tape`，由 approver 根据 tapeName 前缀判断群/单

---

### 1. 插件职责拆分（建议的内部模块）

1. `EventReceiver`
   - 负责飞书 WebSocket / 事件回调注册
   - 负责解析入站事件，提取：
     - `chatType`：group 还是 p2p
     - `chatID`：群的 chatId 或单聊的会话 id（以你们实际 tape 命名为准）
     - `senderID`
     - `triggerMessageID`：`event.Event.Message.MessageId`（用于 thread reply）
     - `messageID`：用于 dedup
     - `inThread`（是否属于飞书原生 thread）
     - `threadRootKey`（线程稳定标识，用于 per-thread isolation & tapeName）
2. `ThreadQueue`
   - 负责队列化执行：同一 `threadKey` 串行，不同 `threadKey` 并行
   - 内部结构建议：
     - `map[string]*worker`：key = `threadKey`
     - 每个 worker 内 `chan job`，worker 串行处理 job
3. `TapeAnchorController`
   - 负责 periodic anchor 移动策略
   - per-thread 状态建议：
     - `lastHistoryAnchorEntryID`：当前使用的 `HistoryAfterEntryID` 起点
     - `moveOnNext`：是否在下一次触发前移动 anchor
4. `DMRClient`
   - 负责调用 host 的 reverse RPC：
     - `Plugin.RunAgent`
     - `Plugin.TapeHandoff`（当需要移动 anchor 时）
5. `FeishuReplySender`
   - 负责回写 agent 输出：
     - `inThread=true`：调用 `/im/v1/messages/{triggerMessageID}/reply` 并设置 `reply_in_thread=true`
     - `inThread=false`：调用 `Im.V1.Message.Create` 发主聊天（ReceiveId = chatID）
6. `FeishuApprover`
   - 实现 `proto.DMRPluginInterface.RequestApproval` / `RequestBatchApproval`
   - 根据 `ApprovalRequest.Tape` 前缀判断群/单
   - 单聊：发送审批提示到对应会话，并等待 `y/s/a/n` 文本响应

---

### 2. 线程识别与 tapeName 设计

1. `threadKey`（队列串行 key）
   - 推荐：稳定线程根 key（`threadRootKey`）
   - 若无法可靠提取稳定 root，就退化为 `triggerMessageID`（隔离更强但可能丢上下文连续性）

2. `tapeName`（DMR context 存储隔离）
   - 推荐 per-thread 独立 tape（最稳并行隔离）：
     - group thread：`feishu:group:<chatID>:thread:<threadRootKey>`
     - group main（非 thread）：可选 `feishu:group:<chatID>:main`（或同样用 triggerMessageID）
     - p2p：`feishu:p2p:<peerOrChatID>`

> 注意：`Tape` 透传给 approver 后，feishu approver 只需要检查 tapeName 前缀：
> - `feishu:group:` => Denied
> - `feishu:p2p:` => 走审批 UI/提示

---

### 3. 入站处理：去重 + 串行队列

入站事件处理伪代码（同一 `threadKey` 串行）：

```text
onP2MessageReceiveV1(event):
  if event.messageID in dedupCache:
    return
  dedupCache.put(event.messageID)

  extract thread info:
    inThread, threadRootKey, triggerMessageID, senderID, chatType, chatID, content

  threadKey = compute(threadRootKey, chatType, chatID fallback)

  enqueue threadKey worker(job={
    tapeName, inThread, triggerMessageID,
    senderID, content
  })
```

---

### 4. 调用 DMR agent：periodic anchor 移动

#### 4.1 anchor 起点如何影响上下文读取

- 当你在 `RunAgent` 前调用 `Plugin.TapeHandoff(...)` 得到 `anchorEntryID`：
- 然后 `RunAgentRequest.HistoryAfterEntryID = anchorEntryID`
- DMR 会在 host 侧读取 tape 时使用 `id > anchorEntryID` 作为上下文切片边界

#### 4.2 periodic 策略（避免 token 打爆）

你选的策略：
- `periodic anchor`：当 `PromptTokens >= ContextBudget * 0.8` 时，为下一次触发移动 anchor

实现建议：
1. 初始状态（每个 thread 首次触发）：
   - 写一个 anchor：`name = "thread:<threadRootKey>:anchor:<ts>"`（state 可加 reason/source）
   - `lastHistoryAnchorEntryID = anchorEntryID`
2. 每次调用结束：
   - 读取 `RunAgentResponse.PromptTokens` 与 `RunAgentResponse.ContextBudget`
   - 若两者满足阈值：设置 `moveOnNext=true`
3. 下一次触发前：
   - 若 `moveOnNext=true`：
     - 调用 `TapeHandoff` 写新 anchor
     - 更新 `lastHistoryAnchorEntryID`，清空 `moveOnNext`
   - 用新的 `lastHistoryAnchorEntryID` 做 `HistoryAfterEntryID`

---

### 5. agent 输出回写：thread reply vs 主聊天

处理逻辑：
1. `inThread=true`：
   - 使用 `triggerMessageID` 作为回复目标
   - 调用 reply endpoint，设置 `reply_in_thread=true`
2. `inThread=false`：
   - 直接 `Message.Create` 到 `ReceiveId = chatID`

---

### 6. 工具审批：群聊 Denied，单聊文本选择

#### 6.1 群聊 `require_approval` 的行为

- 群聊触发的工具审批请求会到 `feishu_approver`
- `feishu_approver` 解析 `ApprovalRequest.Tape`：
  - 若 `Tape` 前缀是 `feishu:group:`：
    - 返回 `ApprovalChoice = Denied`
    - 不发送审批 UI / 不等待输入

这样 opapolicy 会把 `Denied` 变为工具调用拒绝错误，整体效果等价于“群聊不支持审批”。

#### 6.2 单聊审批的交互方式（纯文本 y/s/a/n）

单聊时：
1. `feishu_approver` 在 `RequestApproval` 收到请求后：
   - 解析 `Tape` 前缀 `feishu:p2p:`，得到接收会话 id
   - 生成一个审批请求 token：`reqID`（UUID）
   - 发送消息（建议包含 token，便于匹配后续用户回复）：
     - `Approval Required: <tool> ... token=<reqID>`
     - `y/s/a/n`
2. `EventReceiver` 在后续消息事件中：
   - 检测消息内容是否是 `reqID=y|s|a|n` 或包含 token 的选择格式
   - 找到 pending map 中的 `reqID`，把选择结果送给 `feishu_approver` 的等待通道

> 这要求：审批提示的 token 与用户回写消息能被可靠匹配。

---

### 7. 协议改动如何落到 feishu_approver

本次你已经做过的协议改动（在 DMR 主仓库）：
- `ApprovalRequest` / `proto.ApprovalRequest` 增加 `Tape string`
- `plugins/opapolicy` 在发起 approval 时填入：
  - 单个：`Tape = toolCtx.Tape`
  - 批量：`Tape = item.Ctx.Tape`
- `pkg/plugin/external.go` 负责把内部 `Tape` 透传到 RPC 请求的 `proto.ApprovalRequest.Tape`

feishu_approver 在实现 `RequestApproval` 时只需用 `req.Tape`：
- `feishu:group:` => Denied
- `feishu:p2p:` => send approval prompt + await response

---

### 8. 与现有策略的一致性检查（上线前必须做）

1. Thread isolation：
   - 不同 threadKey 必须进入不同 tapeName（或至少不同 anchor 切片起点）
   - 同一 threadKey 串行队列保证 tape 写入与读取不交错
2. Reply mapping：
   - thread reply 目标必须用“触发消息的 message_id”
   - main chat reply 目标必须用“群 chatID”
3. Approval mapping：
   - 群聊：确保返回 Denied，不会出现审批 UI
   - 单聊：确保 token 匹配成功，避免丢失用户选择

---

### 9. 建议的最小可用（MVP）实现顺序

1. 仅实现入站 -> RunAgent -> main chat 回写（先跑通 agent）
2. 加上 `inThread` 判断，并实现 thread reply 回写
3. 加上 per-thread queue（并发隔离）
4. 加上 periodic anchor 移动（缓解 token 打爆）
5. 最后实现 `require_approval`：
   - 群聊 Denied
   - 单聊发送审批提示 + 等待文本选择

