# 飞书插件多机器人支持设计方案

## 背景

当前飞书插件只支持单个机器人实例。需要支持在一个插件进程中管理多个飞书机器人，以便：
- 不同团队使用不同的机器人（ops、dev、prod 等）
- 每个机器人有独立的 app_id、app_secret、权限控制
- 100 个机器人场景下保持良好性能

## 核心设计原则

### 1. Tape 命名规则保持不变

```
feishu:p2p:{chat_id}
```

**关键洞察**：`chat_id` 在飞书中是全局唯一的。不同机器人收到同一个用户的消息，`chat_id` 是不同的（因为是不同的私聊会话）。因此不需要在 tape 中加入 bot_id。

### 2. 单进程多实例架构

- 一个插件进程管理多个 `BotInstance`
- 每个 `BotInstance` 有独立的 lark client 和 websocket 连接
- 通过 `chat_id -> BotInstance` 映射实现动态路由

### 3. 消息队列按 chat_id 并行 + 空闲超时

- **同一 chat_id 串行**：保证消息顺序
- **不同 chat_id 并行**：不同聊天互不阻塞
- **空闲超时**：自动回收闲置的 worker goroutine

### 4. Tool 名称统一

所有 bot 实例共享相同的 tool 名称（`feishuSendFile`, `feishuSendText`），通过 tape context 自动路由到正确的 bot。

## 配置格式

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "~/.dmr/plugins/dmr-plugin-feishu"

# 全局配置
[plugins.config]
approval_timeout_sec = 300
send_file_root = "~/reports"
inbound_media_enabled = true

# 多个机器人实例
[[plugins.config.bots]]
app_id = "cli_aaa..."
app_secret = "secret:..."
verification_token = "..."
encrypt_key = "..."
allow_from = ["u_ops1", "u_ops2"]

[[plugins.config.bots]]
app_id = "cli_bbb..."
app_secret = "secret:..."
verification_token = "..."
encrypt_key = "..."
allow_from = ["u_dev1"]

[[plugins.config.bots]]
app_id = "cli_ccc..."
app_secret = "secret:..."
allow_from = ["u_prod1"]
```

### 向后兼容

支持旧的单 bot 配置格式，解析时自动转换为 `bots` 数组：

```toml
# 旧格式（继续支持）
[[plugins]]
name = "feishu"
[plugins.config]
app_id = "cli_xxx..."
app_secret = "secret:..."
allow_from = ["u1"]
```

## 数据结构设计

### Config 结构

```go
// config.go
type BotConfig struct {
    AppID             string   `json:"app_id"`
    AppSecret         string   `json:"app_secret"`
    VerificationToken string   `json:"verification_token"`
    EncryptKey        string   `json:"encrypt_key"`
    AllowFrom         []string `json:"allow_from"`
}

type FeishuConfig struct {
    PluginName string      `json:"plugin_name"` // DMR 注入
    Bots       []BotConfig `json:"bots"`

    // --- 以下为全局配置（所有 bot 共享）---
    // 旧的单 bot 字段保留用于向后兼容解析
    AppID             string   `json:"app_id"`
    AppSecret         string   `json:"app_secret"`
    VerificationToken string   `json:"verification_token"`
    EncryptKey        string   `json:"encrypt_key"`
    AllowFrom         []string `json:"allow_from"`

    ApprovalTimeoutSec            int    `json:"approval_timeout_sec"`
    DedupTTLMinutes               int    `json:"dedup_ttl_minutes"`
    SendFileMaxBytes              int    `json:"-"`
    SendFileMaxBytesRaw           any    `json:"send_file_max_bytes"`
    SendFileRoot                  string `json:"send_file_root"`
    Workspace                     string `json:"workspace"`
    ConfigBaseDir                 string `json:"config_base_dir"`
    ExtraPrompt                   string `json:"extra_prompt"`
    ExtraPromptFile               string `json:"extra_prompt_file"`
    InboundMediaEnabled           bool   `json:"inbound_media_enabled"`
    InboundMediaMaxBytes          int    `json:"-"`
    InboundMediaMaxBytesRaw       any    `json:"inbound_media_max_bytes"`
    InboundMediaDir               string `json:"inbound_media_dir"`
    InboundMediaTimeoutSec        int    `json:"inbound_media_timeout_sec"`
    InboundMediaRetentionDays     int    `json:"inbound_media_retention_days"`
    InboundReplyContextEnabled    bool   `json:"inbound_reply_context_enabled"`
    InboundReplyContextTimeoutSec int    `json:"inbound_reply_context_timeout_sec"`
    InboundReplyContextMaxRunes   int    `json:"inbound_reply_context_max_runes"`
    DmrRestartTrigger             string `json:"dmr_restart_trigger"`
    DmrRestartToken               string `json:"dmr_restart_token"`
}

func parseFeishuConfig(jsonStr string) (FeishuConfig, error) {
    cfg := defaultFeishuConfig()
    json.Unmarshal([]byte(jsonStr), &cfg)

    // 向后兼容：如果没有 bots 数组但有 app_id，转换为单 bot
    if len(cfg.Bots) == 0 && cfg.AppID != "" {
        cfg.Bots = []BotConfig{{
            AppID:             cfg.AppID,
            AppSecret:         cfg.AppSecret,
            VerificationToken: cfg.VerificationToken,
            EncryptKey:        cfg.EncryptKey,
            AllowFrom:         cfg.AllowFrom,
        }}
    }

    if len(cfg.Bots) == 0 {
        return cfg, fmt.Errorf("no bots configured: provide bots[] or app_id/app_secret")
    }

    // ... 其他字段的默认值处理保持不变
    return cfg, nil
}
```

### Plugin 结构

```go
// plugin.go
type BotInstance struct {
    cfg      BotConfig
    lc       *lark.Client
    wsClient *larkws.Client
    approver *FeishuApprover
}

type FeishuPlugin struct {
    cfg FeishuConfig

    // 多个 bot 实例
    botsMu sync.RWMutex
    bots   []*BotInstance

    // chat_id -> bot 的动态路由映射（收到消息时建立）
    routingMu sync.RWMutex
    routing   map[string]*BotInstance

    // 全局共享
    hostMu     sync.Mutex
    hostClient *rpc.Client
    dedup      *deduper
    queues     *queueManager

    runMu    sync.Mutex
    runCtx   context.Context
    cancel   context.CancelFunc
    shutdown sync.Once

    activeJobMu sync.Mutex
    activeJob   *inboundJob

    extraRunPrompt string
}
```

### Job 结构

```go
// queue.go
type inboundJob struct {
    QueueKey         string
    TapeName         string
    ChatID           string
    Bot              *BotInstance // 关联的 bot 实例
    SenderID         string
    Content          string
    TriggerMessageID string
    ChatType         string
    ThreadKey        string
    InThread         bool
}
```

## 核心流程

### 1. Init 流程

```go
func (p *FeishuPlugin) Init(req *proto.InitRequest, resp *proto.InitResponse) error {
    cfg, err := parseFeishuConfig(req.ConfigJSON)
    if err != nil {
        return err
    }
    p.cfg = cfg

    p.routing = make(map[string]*BotInstance)
    p.dedup = newDeduper(cfg.dedupTTL())
    p.queues = newQueueManager(p)

    ctx, cancel := context.WithCancel(context.Background())
    p.runCtx = ctx
    p.cancel = cancel

    resolvedExtra, _ := buildResolvedExtraPrompt(cfg)
    p.extraRunPrompt = resolvedExtra

    for i, botCfg := range cfg.Bots {
        bot := &BotInstance{
            cfg: botCfg,
            lc:  lark.NewClient(botCfg.AppID, botCfg.AppSecret),
        }
        bot.approver = newFeishuApprover(p, bot)

        // 每个 bot 的消息处理器（闭包捕获 bot）
        dispatcher := larkdispatcher.NewEventDispatcher(
            botCfg.VerificationToken,
            botCfg.EncryptKey,
        ).OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
            return p.handleMessageReceive(ctx, bot, event)
        })

        bot.wsClient = larkws.NewClient(
            botCfg.AppID,
            botCfg.AppSecret,
            larkws.WithEventHandler(dispatcher),
        )

        p.bots = append(p.bots, bot)

        go func(b *BotInstance, idx int) {
            log.Printf("feishu: starting bot #%d (app_id=%s...)", idx, b.cfg.AppID[:6])
            if err := b.wsClient.Start(ctx); err != nil && ctx.Err() == nil {
                log.Printf("feishu: bot #%d stopped: %v", idx, err)
            }
        }(bot, i)
    }

    p.scheduleInboundRetentionCleanup()
    log.Printf("feishu: initialized %d bots", len(p.bots))
    return nil
}
```

### 2. 消息接收 & 路由映射

```go
// receiver.go
func (p *FeishuPlugin) handleMessageReceive(ctx context.Context, bot *BotInstance, event *larkim.P2MessageReceiveV1) error {
    message := event.Event.Message
    chatID := stringValue(message.ChatId)
    if chatID == "" {
        return nil
    }

    // 建立 chat_id -> bot 的路由映射
    p.registerChatRoute(chatID, bot)

    // 去重
    msgID := stringValue(message.MessageId)
    if p.dedup != nil && p.dedup.isDuplicate(msgID) {
        return nil
    }

    // 构建消息内容（这些方法需要 bot.lc，见下文"API 调用改造"）
    userText := p.buildInboundUserContent(ctx, bot, message)
    modelContent := p.mergeInboundReplyContext(ctx, bot, message, userText)

    // 审批回复消费
    if bot.approver != nil && bot.approver.TryResolveP2P(chatID, userText) {
        return nil
    }

    // 权限检查（使用 bot 自己的 allow_from）
    senderID := extractFeishuSenderID(event.Event.Sender)
    if !isAllowedSender(bot.cfg.AllowFrom, senderID) {
        return nil
    }

    // ... 其他逻辑保持不变 ...

    tape := tapeNameForP2P(chatID)
    job := &inboundJob{
        QueueKey: tape,
        TapeName: tape,
        ChatID:   chatID,
        Bot:      bot,
        SenderID: senderID,
        Content:  modelContent,
        // ...
    }
    p.queues.enqueue(job)
    return nil
}

func (p *FeishuPlugin) registerChatRoute(chatID string, bot *BotInstance) {
    p.routingMu.Lock()
    defer p.routingMu.Unlock()
    p.routing[chatID] = bot
}

func (p *FeishuPlugin) getBotForChat(chatID string) (*BotInstance, error) {
    p.routingMu.RLock()
    defer p.routingMu.RUnlock()
    bot, ok := p.routing[chatID]
    if !ok {
        return nil, fmt.Errorf("no bot found for chat_id: %s", chatID)
    }
    return bot, nil
}
```

### 3. 消息队列：按 chat_id 并行 + 空闲超时

替换当前的单 worker 全局队列：

```go
// queue.go
type queueManager struct {
    plugin *FeishuPlugin

    mu       sync.Mutex
    workers  map[string]chan *inboundJob // chat_id -> job channel
    shutdown bool
    wg       sync.WaitGroup
}

func newQueueManager(p *FeishuPlugin) *queueManager {
    return &queueManager{
        plugin:  p,
        workers: make(map[string]chan *inboundJob),
    }
}

func (qm *queueManager) enqueue(job *inboundJob) {
    if job == nil || job.ChatID == "" {
        return
    }

    qm.mu.Lock()
    if qm.shutdown {
        qm.mu.Unlock()
        return
    }

    ch, exists := qm.workers[job.ChatID]
    if !exists {
        ch = make(chan *inboundJob, 16)
        qm.workers[job.ChatID] = ch
        qm.wg.Add(1)
        go qm.runWorkerForChat(job.ChatID, ch)
        log.Printf("feishu: queue worker started for chat_id=%q", job.ChatID)
    }
    qm.mu.Unlock()

    select {
    case ch <- job:
    default:
        log.Printf("feishu: queue full for chat_id=%q, dropping job", job.ChatID)
    }
}

func (qm *queueManager) runWorkerForChat(chatID string, jobs <-chan *inboundJob) {
    defer qm.wg.Done()
    defer func() {
        qm.mu.Lock()
        delete(qm.workers, chatID)
        qm.mu.Unlock()
        log.Printf("feishu: queue worker stopped for chat_id=%q", chatID)
    }()

    idleTimeout := 5 * time.Minute
    timer := time.NewTimer(idleTimeout)
    defer timer.Stop()

    for {
        select {
        case job, ok := <-jobs:
            if !ok {
                return
            }
            if job != nil {
                qm.plugin.processJob(job)
            }
            if !timer.Stop() {
                select {
                case <-timer.C:
                default:
                }
            }
            timer.Reset(idleTimeout)

        case <-timer.C:
            log.Printf("feishu: queue worker idle timeout for chat_id=%q", chatID)
            return
        }
    }
}

func (qm *queueManager) shutdownAll() {
    qm.mu.Lock()
    if qm.shutdown {
        qm.mu.Unlock()
        return
    }
    qm.shutdown = true
    for _, ch := range qm.workers {
        close(ch)
    }
    qm.mu.Unlock()
    qm.wg.Wait()
}
```

### 4. API 调用改造

当前所有飞书 API 调用都通过 `p.lc`（FeishuPlugin 上的单一 lark client）。多 bot 改造后，需要改为使用 `BotInstance.lc`。

**方案：将所有发送/接收方法改为接收 `bot *BotInstance` 参数。**

涉及的方法及其当前位置：

| 文件 | 方法 | 改造方式 |
|------|------|----------|
| `reply.go` | `sendTextToChat` | 改为 `bot.sendTextToChat` |
| `reply.go` | `sendMarkdownPostToChat` | 改为 `bot.sendMarkdownPostToChat` |
| `reply.go` | `sendApprovalMessageToChat` | 改为 `bot.sendApprovalMessageToChat` |
| `reply.go` | `deliverIMTextForJob` | 使用 `job.Bot.lc` |
| `reply.go` | `deliverIMTextToP2PChat` | 接收 `bot` 参数 |
| `reply.go` | `replyAgentOutput` | 使用 `job.Bot.lc` |
| `file_send.go` | `uploadFileToFeishu` | 改为 `bot.uploadFileToFeishu` |
| `file_send.go` | `sendFileForJob` | 使用 `job.Bot.lc` |
| `file_send.go` | `sendFileFromReader` | 使用 `job.Bot.lc` |
| `receive_media.go` | `downloadMessageResource` | 接收 `bot` 参数 |
| `reply_context.go` | `fetchParentMessage` | 接收 `bot` 参数 |

示例改造：

```go
// reply.go — 改为 BotInstance 的方法
func (b *BotInstance) sendTextToChat(ctx context.Context, chatID, text string) error {
    if b.lc == nil {
        return fmt.Errorf("feishu client not initialized")
    }
    // ... 使用 b.lc 发送，逻辑不变
}

// reply.go — job 相关的方法使用 job.Bot
func (p *FeishuPlugin) replyAgentOutput(ctx context.Context, job *inboundJob, output string) error {
    text := truncateRunes(output, maxFeishuTextRunes)
    return job.Bot.deliverIMTextForJob(ctx, job, text, true)
}
```

### 5. Tool 实现

Tool 名称保持不变，通过 `activeJob.Bot` 或 `getBotForChat()` 路由到正确的 bot：

```go
// plugin.go — ProvideTools 不变
func (p *FeishuPlugin) ProvideTools(req *proto.ProvideToolsRequest, resp *proto.ProvideToolsResponse) error {
    resp.Tools = []proto.ToolDef{
        {Name: "feishuSendFile", Description: "..."},
        {Name: "feishuSendText", Description: "..."},
    }
    return nil
}

// plugin.go — CallTool 不变
func (p *FeishuPlugin) CallTool(req *proto.CallToolRequest, resp *proto.CallToolResponse) error {
    switch req.Name {
    case "feishuSendFile":
        result, err := p.execSendFile(ctx, req.ArgsJSON)
        // ...
    case "feishuSendText":
        result, err := p.execSendText(ctx, req.ArgsJSON)
        // ...
    }
}

// send_text_tool.go
func (p *FeishuPlugin) execSendText(ctx context.Context, argsJSON string) (map[string]any, error) {
    // ...

    job := p.getActiveJob()
    if job != nil {
        // 使用 job 关联的 bot
        if err := job.Bot.deliverIMTextForJob(ctx, job, text, markdown); err != nil {
            return nil, err
        }
        return map[string]any{"ok": true, "chat_id": job.ChatID}, nil
    }

    // 无 active job（cron 等场景）：从 tape_name/chat_id 获取 bot
    var chatID string
    switch {
    case tapeName != "":
        id, ok := p2pChatIDFromTape(tapeName)
        if !ok {
            return nil, fmt.Errorf("invalid tape_name")
        }
        chatID = id
    case chatIDArg != "":
        chatID = chatIDArg
    default:
        return nil, fmt.Errorf("requires tape_name or chat_id")
    }

    bot, err := p.getBotForChat(chatID)
    if err != nil {
        return nil, err
    }

    if err := bot.deliverIMTextToP2PChat(ctx, chatID, text, markdown); err != nil {
        return nil, err
    }
    return map[string]any{"ok": true, "chat_id": chatID}, nil
}
```

### 6. Approver 适配

每个 BotInstance 持有独立的 approver：

```go
// approver.go
type FeishuApprover struct {
    plugin *FeishuPlugin
    bot    *BotInstance
    mu     sync.Mutex
    wait   map[string]*approvalWait
}

func newFeishuApprover(p *FeishuPlugin, bot *BotInstance) *FeishuApprover {
    return &FeishuApprover{
        plugin: p,
        bot:    bot,
        wait:   make(map[string]*approvalWait),
    }
}

// 审批消息通过 bot 的 lark client 发送
func (a *FeishuApprover) waitApproval(chatID, prompt string, batchN int) feishuApprovalReply {
    // ...
    if err := a.bot.sendApprovalMessageToChat(ctx, chatID, prompt); err != nil {
        return feishuApprovalReply{choice: choiceDenied}
    }
    // ...
}

// plugin.go — RequestApproval 通过路由找到正确的 bot
func (p *FeishuPlugin) RequestApproval(req *proto.ApprovalRequest, resp *proto.ApprovalResult) error {
    chatID, ok := p2pChatIDFromTape(req.Tape)
    if !ok {
        resp.Choice = choiceDenied
        return nil
    }
    bot, err := p.getBotForChat(chatID)
    if err != nil {
        resp.Choice = choiceDenied
        return nil
    }
    bot.approver.handleSingle(req, resp)
    return nil
}
```

## 不需要修改的文件

| 文件 | 原因 |
|------|------|
| `parse.go` | `tapeNameForP2P()` / `p2pChatIDFromTape()` 保持不变 |
| `extra_prompt.go` | tool 名称不变，prompt 内容不变 |
| `send_file_tool.go` | 参数 schema 不变 |
| `dedup.go` | 全局去重，不区分 bot |
| `filter.go` | 不涉及 bot 区分 |

## 需要修改的文件

| 文件 | 改动 |
|------|------|
| `config.go` | 添加 `BotConfig`，`FeishuConfig.Bots`，向后兼容解析 |
| `plugin.go` | 移除 `p.lc`/`p.wsClient`，添加 `bots`/`routing`，`Init` 循环创建 bot |
| `receiver.go` | `handleMessageReceive` 接收 `bot` 参数，调用 `registerChatRoute` |
| `queue.go` | `inboundJob` 添加 `Bot` 字段；按 chat_id 并行 + 空闲超时 |
| `reply.go` | 发送方法改为 `BotInstance` 的方法，使用 `b.lc` |
| `file_send.go` | 上传/发送方法改为 `BotInstance` 的方法 |
| `receive_media.go` | `downloadMessageResource` 接收 `bot` 参数 |
| `reply_context.go` | `fetchParentMessage` 接收 `bot` 参数 |
| `send_text_tool.go` | `execSendText` 通过 `job.Bot` 或 `getBotForChat` 获取 bot |
| `send_file_tool.go` | `execSendFile` 使用 `job.Bot` |
| `approver.go` | `FeishuApprover` 持有 `bot`，审批消息通过 bot 发送 |

## 性能特性

### 并发模型

| 场景 | 数量 | 说明 |
|------|------|------|
| 100 个 bot | 100 websocket goroutine | 每个 bot 一个连接 |
| 每 bot 5 个活跃用户 | 500 worker goroutine | 按需创建 |
| 空闲 5 分钟 | 自动退出 | goroutine 自动回收 |
| 内存占用 | ~1 MB | 500 goroutine × 2 KB |

### 并行性保证

```
Bot A, User 1 (chat_id=oc_aaa) → worker_oc_aaa (串行)
Bot A, User 2 (chat_id=oc_bbb) → worker_oc_bbb (串行)
Bot B, User 3 (chat_id=oc_ccc) → worker_oc_ccc (串行)
                                   ↕ 三者完全并行
```

## 风险和注意事项

### 1. 路由映射的冷启动

只有收到消息时才建立 `chat_id -> bot` 映射。如果 cron 任务触发时用户从未向该 bot 发过消息，`getBotForChat()` 会返回错误。

**缓解**：cron 场景下的 tool 调用会失败并返回有意义的错误信息，提示用户先向 bot 发送一条消息建立路由。

### 2. routing map 增长

`routing` map 不会自动清理，长期运行会持续增长。

**缓解**：每个 entry 仅占 ~100 字节（string + pointer），10000 个聊天约 1 MB。如有需要，未来可添加 LRU 或定期清理。

### 3. 错误隔离

一个 bot 的 websocket 断开不影响其他 bot（每个 bot 独立 goroutine）。

### 4. 向后兼容

单 bot 旧配置自动转换为 `bots[0]`，行为完全不变。

## 测试计划

### 单元测试

- `parseFeishuConfig` 解析多 bot 配置
- `parseFeishuConfig` 旧格式自动转换
- `registerChatRoute` / `getBotForChat` 路由正确性
- queue 并行性（不同 chat_id 并行执行）
- queue 串行性（同一 chat_id 按序执行）
- queue 空闲超时自动退出

### 集成测试

- 2 个 bot 分别收到消息，验证各自使用正确的 lark client
- 同时向 2 个 bot 发消息，验证并行处理
- tool 调用（feishuSendText）使用正确的 bot
- 审批流程使用正确的 bot
