package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/rpc"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/seanly/dmr-plugin-feishu/internal/bot"
	"github.com/seanly/dmr-plugin-feishu/internal/dmr"
	"github.com/seanly/dmr-plugin-feishu/internal/inbound"
	"github.com/seanly/dmr-plugin-feishu/internal/prompt"
	"github.com/seanly/dmr-plugin-feishu/internal/queue"
	"github.com/seanly/dmr-plugin-feishu/internal/tools"
	"github.com/seanly/dmr-plugin-feishu/pkg/utils"
	"github.com/seanly/dmr/pkg/plugin/proto"
)

// WebSocket reconnection constants
const (
	wsInitialRetryDelay = 5 * time.Second
	wsMaxRetryDelay     = 5 * time.Minute
	wsRetryMultiplier   = 2.0
)

// Plugin implements proto.DMRPluginInterface.
type Plugin struct {
	cfg Config

	// Multi-bot instances
	botsMu sync.RWMutex
	bots   []*bot.Instance

	// Dynamic routing: chat_id -> bot instance
	routingMu sync.RWMutex
	routing   map[string]*bot.Instance

	// DMR RPC client
	hostMu     sync.Mutex
	hostClient *dmr.Client

	// Lifecycle
	runMu    sync.Mutex
	runCtx   context.Context
	cancel   context.CancelFunc
	shutdown sync.Once

	// Components
	dedup  *inbound.Deduper
	queues *queue.Manager

	// Extra prompt
	extraRunPrompt string
	promptComposer *prompt.Composer
}

// New creates a new Feishu plugin.
func New() *Plugin {
	p := &Plugin{
		cfg:     DefaultConfig(),
		routing: make(map[string]*bot.Instance),
	}
	p.queues = queue.NewManager(p)
	return p
}

// SetHostClient implements the host client setter interface.
func (p *Plugin) SetHostClient(client any) {
	c, ok := client.(*rpc.Client)
	if !ok || c == nil {
		log.Printf("feishu: SetHostClient: unexpected client type %T", client)
		return
	}
	p.hostMu.Lock()
	p.hostClient = dmr.NewClient(c)
	p.hostMu.Unlock()
	log.Printf("feishu: host RPC client attached")
}

// Init initializes the plugin.
func (p *Plugin) Init(req *proto.InitRequest, resp *proto.InitResponse) error {
	cfg, err := ParseConfig(req.ConfigJSON)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	p.cfg = cfg

	if len(cfg.Bots) == 0 {
		return fmt.Errorf("feishu: no bots configured")
	}

	resolvedExtra, err := BuildResolvedExtraPrompt(cfg)
	if err != nil {
		return fmt.Errorf("feishu: %w", err)
	}
	p.extraRunPrompt = resolvedExtra
	p.promptComposer = prompt.NewComposer(resolvedExtra)

	if resolvedExtra != "" {
		log.Printf("feishu: extra run prompt enabled (%d bytes)", len(resolvedExtra))
	}

	p.dedup = inbound.NewDeduper(cfg.GetDedupTTL())

	p.runMu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.runCtx = ctx
	p.cancel = cancel
	p.runMu.Unlock()

	// Create bot instances
	for i, botCfg := range cfg.Bots {
		if botCfg.AppID == "" || botCfg.AppSecret == "" {
			return fmt.Errorf("feishu: bot #%d missing app_id or app_secret", i)
		}

		inst := &bot.Instance{
			Config: bot.ClientConfig{
				AppID:             botCfg.AppID,
				AppSecret:         botCfg.AppSecret,
				VerificationToken: botCfg.VerificationToken,
				EncryptKey:        botCfg.EncryptKey,
				AllowFrom:         botCfg.AllowFrom,
			},
			Client:         bot.NewClient(botCfg.AppID, botCfg.AppSecret),
			GroupEnabled:   botCfg.GroupEnabled,
			ApproverOpenID: botCfg.Approver,
		}
		inst.Approver = bot.NewApprover(p)

		// Auto-fetch bot_id at startup (for group @mention detection)
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
		fetchedID, err := inst.Client.GetBotID(fetchCtx)
		fetchCancel()
		if err != nil {
			log.Printf("feishu: bot #%d bot_open_id=NOT_AVAILABLE (auto-fetch failed: %v)", i, err)
			log.Printf("feishu: bot #%d NOTE: Group chat @mention detection will not work without bot_open_id", i)
		} else {
			inst.BotID = fetchedID
			log.Printf("feishu: bot #%d bot_open_id=%s (auto-fetched, group @mention enabled)", i, inst.BotID)
		}

		// Each bot's message handler
		botInst := inst // capture for closure
		dispatcher := larkdispatcher.NewEventDispatcher(botCfg.VerificationToken, botCfg.EncryptKey).
			OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
				return p.handleMessageReceive(ctx, botInst, event)
			})

		inst.WSClient = larkws.NewClient(botCfg.AppID, botCfg.AppSecret, larkws.WithEventHandler(dispatcher))

		p.bots = append(p.bots, inst)

		appIDPrefix := botCfg.AppID
		if len(appIDPrefix) > 6 {
			appIDPrefix = appIDPrefix[:6]
		}
		log.Printf("feishu: bot #%d app_id_prefix=%q", i, appIDPrefix)

		// Start WebSocket in background with reconnection
		go p.runWebSocketWithReconnect(ctx, inst.WSClient, dispatcher, i, botCfg)
	}

	log.Printf("feishu: initialized %d bots", len(p.bots))

	// Schedule cleanup
	p.scheduleInboundRetentionCleanup()

	return nil
}

// Shutdown cleans up the plugin.
func (p *Plugin) Shutdown(req *proto.ShutdownRequest, resp *proto.ShutdownResponse) error {
	p.shutdown.Do(func() {
		p.runMu.Lock()
		if p.cancel != nil {
			p.cancel()
			p.cancel = nil
		}
		p.runMu.Unlock()
		if p.queues != nil {
			p.queues.ShutdownAll()
		}
		p.botsMu.Lock()
		p.bots = nil
		p.botsMu.Unlock()
	})
	return nil
}

// RequestApproval handles single approval request.
// For group chats, approval is routed to admin's P2P instead of the group.
func (p *Plugin) RequestApproval(req *proto.ApprovalRequest, resp *proto.ApprovalResult) error {
	tape := strings.TrimSpace(req.Tape)

	// Determine if this is a group chat and get the routing info
	chatID, isGroup := p.resolveApprovalChatID(tape)
	if chatID == "" {
		resp.Choice = bot.ChoiceDenied
		resp.Comment = "unknown tape routing for approval"
		return nil
	}

	botInst, err := p.GetBotForChat(chatID)
	if err != nil {
		resp.Choice = bot.ChoiceDenied
		resp.Comment = "no bot found for chat"
		return nil
	}
	if botInst.Approver == nil {
		resp.Choice = bot.ChoiceDenied
		return nil
	}

	// Get approver open_id for group chat
	var approverOpenID string
	if isGroup {
		approverOpenID = botInst.ApproverOpenID
		if approverOpenID == "" {
			log.Printf("feishu: group approval requested but no approver configured for this bot")
			resp.Choice = bot.ChoiceDenied
			resp.Comment = "no approver configured for group approval"
			return nil
		}
	}

	ctx := p.getRunCtx()

	// Build send function based on chat type
	sendFn := func(prompt string) error {
		if isGroup {
			// Send to approver's P2P
			return botInst.Client.SendApprovalMessageToChat(ctx, approverOpenID, prompt)
		}
		return botInst.Client.SendApprovalMessageToChat(ctx, chatID, prompt)
	}

	// Determine which chat_id to use for waiting approval
	waitChatID := chatID
	if isGroup && approverOpenID != "" {
		waitChatID = approverOpenID
	}

	botInst.Approver.HandleSingle(req, resp, waitChatID, sendFn, p.cfg.GetApprovalTimeout())
	return nil
}

// RequestBatchApproval handles batch approval request.
// For group chats, approval is routed to admin's P2P instead of the group.
func (p *Plugin) RequestBatchApproval(req *proto.BatchApprovalRequest, resp *proto.BatchApprovalResult) error {
	if len(req.Requests) == 0 {
		resp.Choice = bot.ChoiceDenied
		return nil
	}

	tape := strings.TrimSpace(req.Requests[0].Tape)

	// Determine if this is a group chat and get the routing info
	chatID, isGroup := p.resolveApprovalChatID(tape)
	if chatID == "" {
		resp.Choice = bot.ChoiceDenied
		return nil
	}

	botInst, err := p.GetBotForChat(chatID)
	if err != nil {
		resp.Choice = bot.ChoiceDenied
		return nil
	}
	if botInst.Approver == nil {
		resp.Choice = bot.ChoiceDenied
		return nil
	}

	// Get approver open_id for group chat
	var approverOpenID string
	if isGroup {
		approverOpenID = botInst.ApproverOpenID
		if approverOpenID == "" {
			log.Printf("feishu: group batch approval requested but no approver configured for this bot")
			resp.Choice = bot.ChoiceDenied
			return nil
		}
	}

	ctx := p.getRunCtx()

	// Build send function based on chat type
	sendFn := func(prompt string) error {
		if isGroup {
			return botInst.Client.SendApprovalMessageToChat(ctx, approverOpenID, prompt)
		}
		return botInst.Client.SendApprovalMessageToChat(ctx, chatID, prompt)
	}

	// Determine which chat_id to use for waiting approval
	waitChatID := chatID
	if isGroup && approverOpenID != "" {
		waitChatID = approverOpenID
	}

	botInst.Approver.HandleBatch(req, resp, waitChatID, sendFn, p.cfg.GetApprovalTimeout())
	return nil
}

// resolveApprovalChatID determines the chat_id and whether it's a group chat from tape name.
// Returns (chat_id, is_group).
func (p *Plugin) resolveApprovalChatID(tape string) (string, bool) {
	// Try P2P first
	if chatID, ok := inbound.P2PChatIDFromTape(tape); ok {
		return chatID, false
	}

	// Try group chat
	if chatID, ok := inbound.GroupChatIDFromTape(tape); ok {
		return chatID, true
	}

	return "", false
}

// ProvideTools returns the tools provided by this plugin.
func (p *Plugin) ProvideTools(req *proto.ProvideToolsRequest, resp *proto.ProvideToolsResponse) error {
	resp.Tools = []proto.ToolDef{
		{
			Name:           "feishuSendFile",
			Description:    "Upload a local file and send it as a file message in the current p2p chat.",
			ParametersJSON: tools.SendFileParams(),
			Group:          "extended",
			SearchHint:     "feishu, send, file, upload, attachment, document, 飞书, 发送文件, 上传",
		},
		{
			Name:           "feishuSendText",
			Description:    "Send a short non-report text to the current Feishu p2p chat.",
			ParametersJSON: tools.SendTextParams(),
			Group:          "extended",
			SearchHint:     "feishu, send, message, text, chat, im, 飞书, 发送消息, 文本",
		},
	}
	names := []string{"feishuSendFile", "feishuSendText"}
	log.Printf("feishu: ProvideTools -> %s", strings.Join(names, ", "))
	return nil
}

// CallTool executes a tool call.
func (p *Plugin) CallTool(req *proto.CallToolRequest, resp *proto.CallToolResponse) error {
	ctx := p.getRunCtx()

	// Parse context from the request (passed from RunAgent)
	toolCtx := make(map[string]any)
	if req.ContextJSON != "" {
		if err := json.Unmarshal([]byte(req.ContextJSON), &toolCtx); err != nil {
			log.Printf("feishu: CallTool %s failed to parse context JSON: %v", req.Name, err)
		}
	}

	// Extract chat_id from context or session tape
	chatID, _ := toolCtx["chat_id"].(string)
	if chatID == "" {
		// Fallback: try to extract from session tape (e.g., "feishu:p2p:oc_xxx")
		chatID, _ = inbound.P2PChatIDFromTape(req.SessionTape)
	}

	if chatID != "" {
		log.Printf("feishu: CallTool %s chat_id=%q", req.Name, chatID)
	} else {
		log.Printf("feishu: CallTool %s (no chat_id in context or tape %q)", req.Name, req.SessionTape)
	}

	switch req.Name {
	case "feishuSendFile":
		result, err := p.execSendFile(ctx, req.ArgsJSON, toolCtx)
		if err != nil {
			log.Printf("feishu: feishuSendFile failed: %v", err)
			resp.Error = err.Error()
			return nil
		}
		b, _ := json.Marshal(result)
		resp.ResultJSON = string(b)
		return nil
	case "feishuSendText":
		result, err := p.execSendText(ctx, req.ArgsJSON, toolCtx)
		if err != nil {
			log.Printf("feishu: feishuSendText failed: %v", err)
			resp.Error = err.Error()
			return nil
		}
		b, _ := json.Marshal(result)
		resp.ResultJSON = string(b)
		return nil
	default:
		resp.Error = fmt.Sprintf("unknown tool: %s", req.Name)
		return nil
	}
}

// execSendFile executes feishuSendFile tool.
// Uses context passed from RunAgent instead of maintaining in-memory job state.
func (p *Plugin) execSendFile(ctx context.Context, argsJSON string, toolCtx map[string]any) (map[string]any, error) {
	// Extract context values
	chatID, _ := toolCtx["chat_id"].(string)
	triggerMessageID, _ := toolCtx["trigger_message_id"].(string)
	inThread, _ := toolCtx["in_thread"].(bool)

	if chatID == "" {
		return nil, fmt.Errorf("feishuSendFile: chat_id not found in context")
	}

	// Get bot for this chat dynamically
	botInst, err := p.GetBotForChat(chatID)
	if err != nil {
		return nil, fmt.Errorf("feishuSendFile: bot not found for chat %s: %w", chatID, err)
	}
	if botInst == nil || botInst.Client == nil {
		return nil, fmt.Errorf("feishuSendFile: bot client not initialized for chat %s", chatID)
	}

	return tools.ExecuteSendFile(ctx, argsJSON, chatID, inThread, triggerMessageID, botInst.Client, p.cfg.GetSendFileMaxBytes(), p.cfg.SendFileRoot, p.cfg.Workspace)
}

// execSendText executes feishuSendText tool.
// Uses context passed from RunAgent instead of maintaining in-memory job state.
func (p *Plugin) execSendText(ctx context.Context, argsJSON string, toolCtx map[string]any) (map[string]any, error) {
	// Extract context values (may be empty if not provided)
	chatID, _ := toolCtx["chat_id"].(string)
	triggerMessageID, _ := toolCtx["trigger_message_id"].(string)
	inThread, _ := toolCtx["in_thread"].(bool)

	// Get bot for context chat (if available)
	var contextBot tools.ThreadAwareMessageClient
	if chatID != "" {
		if bot, err := p.GetBotForChat(chatID); err == nil && bot != nil {
			contextBot = bot.Client
		}
	}

	// Helper to get bot for any chat ID
	getBotForChat := func(chatID string) (tools.SimpleMessageClient, error) {
		bot, err := p.GetBotForChat(chatID)
		if err != nil {
			return nil, err
		}
		if bot == nil || bot.Client == nil {
			return nil, fmt.Errorf("bot not initialized for chat_id: %s", chatID)
		}
		return bot.Client, nil
	}

	return tools.ExecuteSendText(ctx, argsJSON, chatID, triggerMessageID, inThread, contextBot, getBotForChat)
}

// Helper methods

func (p *Plugin) getRunCtx() context.Context {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	if p.runCtx != nil {
		return p.runCtx
	}
	return context.Background()
}

// RegisterChatRoute registers a chat_id to bot mapping.
func (p *Plugin) RegisterChatRoute(chatID string, botInst interface{}) {
	if b, ok := botInst.(*bot.Instance); ok {
		p.routingMu.Lock()
		defer p.routingMu.Unlock()
		p.routing[chatID] = b
	}
}

// GetBotForChat retrieves the bot instance for a chat_id.
// If not found in routing table, it will try to rebuild by iterating through all bots.
func (p *Plugin) GetBotForChat(chatID string) (*bot.Instance, error) {
	// First check routing table
	p.routingMu.RLock()
	b, ok := p.routing[chatID]
	p.routingMu.RUnlock()
	if ok {
		return b, nil
	}

	// Not found - try to rebuild by iterating through all bots
	return p.findAndRegisterBotForChat(chatID)
}

// findAndRegisterBotForChat iterates through all bots to find which one owns this chat_id.
func (p *Plugin) findAndRegisterBotForChat(chatID string) (*bot.Instance, error) {
	ctx := context.Background()

	p.botsMu.RLock()
	defer p.botsMu.RUnlock()

	for _, b := range p.bots {
		// Try to get chat info to check if this bot has this chat
		exists, err := b.Client.ChatExists(ctx, chatID)
		if err != nil {
			log.Printf("feishu: bot %s failed to check chat %s: %v", b.BotID, chatID, err)
			continue
		}
		if exists {
			// Found - register it
			p.routingMu.Lock()
			p.routing[chatID] = b
			p.routingMu.Unlock()
			log.Printf("feishu: rebuilt routing: chat_id=%s -> bot=%s", chatID, b.BotID)
			return b, nil
		}
	}

	return nil, fmt.Errorf("no bot found for chat_id: %s", chatID)
}

// GetDeduper returns the deduplicator.
func (p *Plugin) GetDeduper() *inbound.Deduper {
	return p.dedup
}

// ComposeRunPrompt composes the run prompt.
func (p *Plugin) ComposeRunPrompt(userContent string) string {
	return p.promptComposer.Compose(userContent)
}

// CallRunAgent calls the DMR RunAgent method.
func (p *Plugin) CallRunAgent(tape, prompt string, historyAfter int64) (*proto.RunAgentResponse, error) {
	return p.CallRunAgentWithContext(tape, prompt, historyAfter, nil)
}

// CallRunAgentWithContext calls the DMR RunAgent method with plugin context.
// The context is passed to tool calls, allowing them to access trigger-time information
// without maintaining in-memory state.
func (p *Plugin) CallRunAgentWithContext(tape, prompt string, historyAfter int64, ctx map[string]any) (*proto.RunAgentResponse, error) {
	p.hostMu.Lock()
	client := p.hostClient
	p.hostMu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("host client not available")
	}
	return client.RunAgentWithContext(tape, prompt, historyAfter, ctx)
}

// ReplyAgentOutput sends the agent output back to Feishu.
// Deprecated: Use ReplyAgentOutputWithContext instead.
func (p *Plugin) ReplyAgentOutput(ctx context.Context, job *queue.Job, output string) error {
	if job == nil {
		return fmt.Errorf("invalid job")
	}
	return p.ReplyAgentOutputWithContext(ctx, job.ChatID, job.TriggerMessageID, job.InThread, output)
}

// ReplyAgentOutputWithContext sends the agent output back to Feishu using context values.
func (p *Plugin) ReplyAgentOutputWithContext(ctx context.Context, chatID, triggerMessageID string, inThread bool, output string) error {
	text := utils.TruncateRunes(output, 18000)
	if chatID == "" {
		return fmt.Errorf("invalid chat_id")
	}
	botInst, err := p.GetBotForChat(chatID)
	if err != nil {
		return fmt.Errorf("bot not found for chat %s: %w", chatID, err)
	}
	return botInst.Client.DeliverIMTextForJob(ctx, chatID, triggerMessageID, inThread, text, true)
}

// TryResolveApproval tries to resolve a message as an approval reply.
func (p *Plugin) TryResolveApproval(chatID, content string) bool {
	botInst, err := p.GetBotForChat(chatID)
	if err != nil {
		// For admin P2P that may not be in routing, try first bot
		p.botsMu.RLock()
		if len(p.bots) > 0 {
			botInst = p.bots[0]
		}
		p.botsMu.RUnlock()
	}
	if botInst == nil || botInst.Approver == nil {
		return false
	}
	return botInst.Approver.TryResolveP2P(chatID, content)
}

// IsAllowedSender checks if a sender is allowed.
func (p *Plugin) IsAllowedSender(allowList []string, senderID string) bool {
	return inbound.IsAllowedSender(allowList, senderID)
}

// SendTextReply sends a text reply to a chat.
func (p *Plugin) SendTextReply(chatID, text string) error {
	botInst, err := p.GetBotForChat(chatID)
	if err != nil {
		return err
	}
	ctx := p.getRunCtx()
	return botInst.Client.SendTextToChat(ctx, chatID, text)
}

// IsGroupEnabledForBot returns whether group chat is enabled for a specific bot.
func (p *Plugin) IsGroupEnabledForBot(botInst interface{}) bool {
	if b, ok := botInst.(*bot.Instance); ok {
		return b.GroupEnabled
	}
	return false
}

// GetApproverOpenID returns the approver's open_id for a specific bot.
func (p *Plugin) GetApproverOpenID(botInst interface{}) string {
	if b, ok := botInst.(*bot.Instance); ok {
		return b.ApproverOpenID
	}
	return ""
}

// GetBotID returns the bot's open_id for mention detection.
// For multi-bot setup, returns the first bot's ID.
func (p *Plugin) GetBotID(botInst interface{}) string {
	if b, ok := botInst.(*bot.Instance); ok {
		return b.BotID
	}
	// Fallback: try first bot
	p.botsMu.RLock()
	defer p.botsMu.RUnlock()
	if len(p.bots) > 0 {
		return p.bots[0].BotID
	}
	return ""
}

// EnqueueJob enqueues a job for processing.
func (p *Plugin) EnqueueJob(job *inbound.Job) {
	if p.queues != nil {
		// Convert inbound.Job to queue.Job
		queueJob := &queue.Job{
			QueueKey:         job.QueueKey,
			TapeName:         job.TapeName,
			ChatID:           job.ChatID,
			Bot:              job.Bot,
			SenderID:         job.SenderID,
			Content:          job.Content,
			TriggerMessageID: job.TriggerMessageID,
			ChatType:         job.ChatType,
			ThreadKey:        job.ThreadKey,
			InThread:         job.InThread,
		}
		p.queues.Enqueue(queueJob)
	}
}

// ProcessJob processes a job.
func (p *Plugin) ProcessJob(job *queue.Job) {
	queue.ProcessJob(p.getRunCtx(), p, job)
}

// BuildInboundUserContent builds user content from a message.
func (p *Plugin) BuildInboundUserContent(ctx context.Context, larkClient *lark.Client, message *larkim.EventMessage, msgID string) string {
	return bot.BuildInboundUserContent(ctx, larkClient, &p.cfg, message, msgID)
}

// MergeInboundReplyContext merges reply context.
func (p *Plugin) MergeInboundReplyContext(ctx context.Context, larkClient *lark.Client, message *larkim.EventMessage, userText string) string {
	return bot.MergeInboundReplyContext(ctx, larkClient, &p.cfg, message, userText)
}

func (p *Plugin) scheduleInboundRetentionCleanup() {
	if p.cfg.GetInboundMediaRetentionDays() <= 0 {
		return
	}
	p.runMu.Lock()
	parent := p.runCtx
	p.runMu.Unlock()
	if parent == nil {
		return
	}
	go func() {
		select {
		case <-parent.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if err := bot.CleanupInboundOldDays(&p.cfg); err != nil {
			log.Printf("feishu: inbound retention cleanup: %v", err)
		}
	}()
}

func (p *Plugin) handleMessageReceive(ctx context.Context, botInst *bot.Instance, event *larkim.P2MessageReceiveV1) error {
	receiver := &inbound.Receiver{Plugin: p}
	return receiver.HandleMessageReceive(ctx, botInst, botInst.Client.Lark, botInst.Config.AllowFrom, event)
}

// runWebSocketWithReconnect runs WebSocket with exponential backoff reconnection.
func (p *Plugin) runWebSocketWithReconnect(parentCtx context.Context, ws *larkws.Client, dispatcher *larkdispatcher.EventDispatcher, idx int, botCfg BotConfig) {
	retryDelay := wsInitialRetryDelay
	
	for {
		select {
		case <-parentCtx.Done():
			log.Printf("feishu: bot #%d websocket loop stopped (context cancelled)", idx)
			return
		default:
		}
		
		log.Printf("feishu: starting bot #%d websocket", idx)
		err := ws.Start(parentCtx)
		
		// Check if shutdown requested
		if parentCtx.Err() != nil {
			log.Printf("feishu: bot #%d websocket stopped (context cancelled)", idx)
			return
		}
		
		if err != nil {
			log.Printf("feishu: bot #%d websocket error: %v, reconnecting in %v...", idx, err, retryDelay)
		}
		
		// Wait before reconnecting
		select {
		case <-time.After(retryDelay):
			// Increase delay for next retry (exponential backoff)
			retryDelay = time.Duration(float64(retryDelay) * wsRetryMultiplier)
			if retryDelay > wsMaxRetryDelay {
				retryDelay = wsMaxRetryDelay
			}
			// Recreate WebSocket client for fresh connection
			ws = larkws.NewClient(botCfg.AppID, botCfg.AppSecret, larkws.WithEventHandler(dispatcher))
		case <-parentCtx.Done():
			log.Printf("feishu: bot #%d websocket loop stopped during retry wait", idx)
			return
		}
	}
}

// Ensure Plugin implements queue.Handler
var _ queue.Handler = (*Plugin)(nil)
