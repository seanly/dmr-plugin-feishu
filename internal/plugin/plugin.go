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

	// Active jobs tracking
	activeJobsMu sync.RWMutex
	activeJobs   map[string]*queue.Job

	// Extra prompt
	extraRunPrompt string
	promptComposer *prompt.Composer
}

// New creates a new Feishu plugin.
func New() *Plugin {
	p := &Plugin{
		cfg:        DefaultConfig(),
		routing:    make(map[string]*bot.Instance),
		activeJobs: make(map[string]*queue.Job),
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
			Client: bot.NewClient(botCfg.AppID, botCfg.AppSecret),
		}
		inst.Approver = bot.NewApprover(p)

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

		// Start WebSocket in background
		go func(ws *larkws.Client, idx int) {
			log.Printf("feishu: starting bot #%d websocket", idx)
			if err := ws.Start(ctx); err != nil && ctx.Err() == nil {
				log.Printf("feishu: bot #%d websocket stopped: %v", idx, err)
			}
		}(inst.WSClient, i)
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
func (p *Plugin) RequestApproval(req *proto.ApprovalRequest, resp *proto.ApprovalResult) error {
	chatID, ok := inbound.P2PChatIDFromTape(strings.TrimSpace(req.Tape))
	if !ok {
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

	ctx := p.getRunCtx()
	sendFn := func(prompt string) error {
		return botInst.Client.SendApprovalMessageToChat(ctx, chatID, prompt)
	}
	botInst.Approver.HandleSingle(req, resp, chatID, sendFn, p.cfg.GetApprovalTimeout())
	return nil
}

// RequestBatchApproval handles batch approval request.
func (p *Plugin) RequestBatchApproval(req *proto.BatchApprovalRequest, resp *proto.BatchApprovalResult) error {
	if len(req.Requests) == 0 {
		resp.Choice = bot.ChoiceDenied
		return nil
	}
	chatID, ok := inbound.P2PChatIDFromTape(strings.TrimSpace(req.Requests[0].Tape))
	if !ok {
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

	ctx := p.getRunCtx()
	sendFn := func(prompt string) error {
		return botInst.Client.SendApprovalMessageToChat(ctx, chatID, prompt)
	}
	botInst.Approver.HandleBatch(req, resp, chatID, sendFn, p.cfg.GetApprovalTimeout())
	return nil
}

// ProvideTools returns the tools provided by this plugin.
func (p *Plugin) ProvideTools(req *proto.ProvideToolsRequest, resp *proto.ProvideToolsResponse) error {
	resp.Tools = []proto.ToolDef{
		{
			Name:           "feishuSendFile",
			Description:    "Upload a local file and send it as a file message in the current p2p chat.",
			ParametersJSON: tools.SendFileParams(),
		},
		{
			Name:           "feishuSendText",
			Description:    "Send a short non-report text to the current Feishu p2p chat.",
			ParametersJSON: tools.SendTextParams(),
		},
	}
	names := []string{"feishuSendFile", "feishuSendText"}
	log.Printf("feishu: ProvideTools -> %s", strings.Join(names, ", "))
	return nil
}

// CallTool executes a tool call.
func (p *Plugin) CallTool(req *proto.CallToolRequest, resp *proto.CallToolResponse) error {
	ctx := p.getRunCtx()

	// Look up the active job
	job := p.GetActiveJobByTape(req.SessionTape)

	switch req.Name {
	case "feishuSendFile":
		result, err := p.execSendFile(ctx, req.ArgsJSON, job)
		if err != nil {
			resp.Error = err.Error()
			return nil
		}
		b, _ := json.Marshal(result)
		resp.ResultJSON = string(b)
		return nil
	case "feishuSendText":
		result, err := p.execSendText(ctx, req.ArgsJSON, job)
		if err != nil {
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
func (p *Plugin) execSendFile(ctx context.Context, argsJSON string, job *queue.Job) (map[string]any, error) {
	var jobChatID string
	var jobInThread bool
	var jobTriggerMessageID string
	var jobBot *bot.Instance

	if job != nil {
		jobChatID = job.ChatID
		jobInThread = job.InThread
		jobTriggerMessageID = job.TriggerMessageID
		if b, ok := job.Bot.(*bot.Instance); ok {
			jobBot = b
		}
	}

	if jobBot == nil {
		return nil, fmt.Errorf("feishuSendFile only works during a Feishu-triggered RunAgent")
	}

	return tools.ExecuteSendFile(ctx, argsJSON, jobChatID, jobInThread, jobTriggerMessageID, jobBot.Client, p.cfg.GetSendFileMaxBytes(), p.cfg.SendFileRoot, p.cfg.Workspace)
}

// execSendText executes feishuSendText tool.
func (p *Plugin) execSendText(ctx context.Context, argsJSON string, job *queue.Job) (map[string]any, error) {
	var jobChatID string
	var jobTriggerMessageID string
	var jobInThread bool
	var jobBot *bot.Instance

	if job != nil {
		jobChatID = job.ChatID
		jobTriggerMessageID = job.TriggerMessageID
		jobInThread = job.InThread
		if b, ok := job.Bot.(*bot.Instance); ok {
			jobBot = b
		}
	}

	getBotForChat := func(chatID string) (tools.SimpleMessageClient, error) {
		bot, err := p.GetBotForChat(chatID)
		if err != nil {
			return nil, err
		}
		return bot.Client, nil
	}

	return tools.ExecuteSendText(ctx, argsJSON, jobChatID, jobTriggerMessageID, jobInThread, jobBot.Client, getBotForChat)
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
func (p *Plugin) GetBotForChat(chatID string) (*bot.Instance, error) {
	p.routingMu.RLock()
	defer p.routingMu.RUnlock()
	b, ok := p.routing[chatID]
	if !ok {
		return nil, fmt.Errorf("no bot found for chat_id: %s", chatID)
	}
	return b, nil
}

// GetDeduper returns the deduplicator.
func (p *Plugin) GetDeduper() *inbound.Deduper {
	return p.dedup
}

// SetActiveJob sets an active job for the given tape.
func (p *Plugin) SetActiveJob(job *queue.Job) {
	p.activeJobsMu.Lock()
	p.activeJobs[job.TapeName] = job
	p.activeJobsMu.Unlock()
}

// ClearActiveJob clears the active job for the given tape.
func (p *Plugin) ClearActiveJob(tapeName string) {
	p.activeJobsMu.Lock()
	delete(p.activeJobs, tapeName)
	p.activeJobsMu.Unlock()
}

// GetActiveJobByTape retrieves the active job for a tape.
func (p *Plugin) GetActiveJobByTape(tapeName string) *queue.Job {
	p.activeJobsMu.RLock()
	defer p.activeJobsMu.RUnlock()
	if job, ok := p.activeJobs[tapeName]; ok {
		return job
	}
	// Try stripping subagent suffix
	parent := inbound.StripDMRSubagentChildTapeSuffix(tapeName)
	if parent != tapeName {
		return p.activeJobs[parent]
	}
	return nil
}

// ComposeRunPrompt composes the run prompt.
func (p *Plugin) ComposeRunPrompt(userContent string) string {
	return p.promptComposer.Compose(userContent)
}

// CallRunAgent calls the DMR RunAgent method.
func (p *Plugin) CallRunAgent(tape, prompt string, historyAfter int64) (*proto.RunAgentResponse, error) {
	p.hostMu.Lock()
	client := p.hostClient
	p.hostMu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("host client not available")
	}
	return client.RunAgent(tape, prompt, historyAfter)
}

// ReplyAgentOutput sends the agent output back to Feishu.
func (p *Plugin) ReplyAgentOutput(ctx context.Context, job *queue.Job, output string) error {
	text := utils.TruncateRunes(output, 18000)
	if job == nil || job.Bot == nil {
		return fmt.Errorf("invalid job")
	}
	botInst, ok := job.Bot.(*bot.Instance)
	if !ok {
		return fmt.Errorf("invalid bot instance")
	}
	return botInst.Client.DeliverIMTextForJob(ctx, job.ChatID, job.TriggerMessageID, job.InThread, text, true)
}

// TryResolveApproval tries to resolve a message as an approval reply.
func (p *Plugin) TryResolveApproval(chatID, content string) bool {
	botInst, err := p.GetBotForChat(chatID)
	if err != nil || botInst.Approver == nil {
		return false
	}
	return botInst.Approver.TryResolveP2P(chatID, content)
}

// IsAllowedSender checks if a sender is allowed.
func (p *Plugin) IsAllowedSender(allowList []string, senderID string) bool {
	return inbound.IsAllowedSender(allowList, senderID)
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

// Ensure Plugin implements queue.Handler
var _ queue.Handler = (*Plugin)(nil)
