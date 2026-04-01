package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/rpc"
	"strings"
	"sync"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// BotInstance holds a single Feishu bot's lark client, websocket, and approver.
type BotInstance struct {
	cfg      BotConfig
	lc       *lark.Client
	wsClient *larkws.Client
	approver *FeishuApprover
}

// FeishuPlugin implements proto.DMRPluginInterface and proto.HostClientSetter.
type FeishuPlugin struct {
	cfg FeishuConfig

	// Multi-bot instances.
	botsMu sync.RWMutex
	bots   []*BotInstance

	// Dynamic routing: chat_id -> bot instance (built on message receive).
	routingMu sync.RWMutex
	routing   map[string]*BotInstance

	hostMu     sync.Mutex
	hostClient *rpc.Client

	runMu    sync.Mutex
	runCtx   context.Context
	cancel   context.CancelFunc
	shutdown sync.Once

	dedup  *deduper
	queues *queueManager

	// activeJob is set for the duration of callRunAgent in processJob so CallTool (e.g. feishuSendFile, feishuSendText)
	// can route to the current Feishu chat/thread. Nil when not inside RunAgent.
	activeJobMu sync.Mutex
	activeJob   *inboundJob

	// extraRunPrompt is built at Init from extra_prompt_file + extra_prompt (Feishu inbound RunAgent only).
	extraRunPrompt string
}

// NewFeishuPlugin builds the plugin implementation used by main.
func NewFeishuPlugin() *FeishuPlugin {
	p := &FeishuPlugin{
		cfg:     defaultFeishuConfig(),
		routing: make(map[string]*BotInstance),
	}
	p.queues = newQueueManager(p)
	return p
}

func (p *FeishuPlugin) setActiveJob(job *inboundJob) {
	p.activeJobMu.Lock()
	p.activeJob = job
	p.activeJobMu.Unlock()
}

func (p *FeishuPlugin) clearActiveJob() {
	p.activeJobMu.Lock()
	p.activeJob = nil
	p.activeJobMu.Unlock()
}

func (p *FeishuPlugin) getActiveJob() *inboundJob {
	p.activeJobMu.Lock()
	defer p.activeJobMu.Unlock()
	return p.activeJob
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

func (p *FeishuPlugin) SetHostClient(client any) {
	c, ok := client.(*rpc.Client)
	if !ok || c == nil {
		log.Printf("feishu: SetHostClient: unexpected client type %T", client)
		return
	}
	p.hostMu.Lock()
	p.hostClient = c
	p.hostMu.Unlock()
	log.Printf("feishu: host RPC client attached")
}

func (p *FeishuPlugin) Init(req *proto.InitRequest, resp *proto.InitResponse) error {
	cfg, err := parseFeishuConfig(req.ConfigJSON)
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	p.cfg = cfg

	if len(cfg.Bots) == 0 {
		return fmt.Errorf("feishu: no bots configured (provide bots[] or legacy app_id/app_secret)")
	}

	resolvedExtra, err := buildResolvedExtraPrompt(cfg)
	if err != nil {
		return fmt.Errorf("feishu: %w", err)
	}
	p.extraRunPrompt = resolvedExtra
	if resolvedExtra != "" {
		log.Printf("feishu: extra run prompt enabled (%d bytes); prepended to inbound RunAgent user message", len(resolvedExtra))
	}

	p.dedup = newDeduper(cfg.dedupTTL())

	p.runMu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.runCtx = ctx
	p.cancel = cancel
	p.runMu.Unlock()

	// Create bot instances.
	for i, botCfg := range cfg.Bots {
		if botCfg.AppID == "" || botCfg.AppSecret == "" {
			return fmt.Errorf("feishu: bot #%d missing app_id or app_secret", i)
		}

		bot := &BotInstance{
			cfg: botCfg,
			lc:  lark.NewClient(botCfg.AppID, botCfg.AppSecret),
		}
		bot.approver = newFeishuApprover(p, bot)

		// Each bot's message handler (closure captures bot).
		dispatcher := larkdispatcher.NewEventDispatcher(botCfg.VerificationToken, botCfg.EncryptKey).
			OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
				return p.handleMessageReceive(ctx, bot, event)
			})

		bot.wsClient = larkws.NewClient(botCfg.AppID, botCfg.AppSecret, larkws.WithEventHandler(dispatcher))
		p.bots = append(p.bots, bot)

		appIDPrefix := botCfg.AppID
		if len(appIDPrefix) > 6 {
			appIDPrefix = appIDPrefix[:6]
		}
		log.Printf("feishu: bot #%d app_id_prefix=%q allow_from=%d", i, appIDPrefix, len(botCfg.AllowFrom))

		go func(b *BotInstance, idx int) {
			log.Printf("feishu: starting bot #%d websocket", idx)
			if err := b.wsClient.Start(ctx); err != nil && ctx.Err() == nil {
				log.Printf("feishu: bot #%d websocket stopped: %v", idx, err)
			}
		}(bot, i)
	}

	log.Printf("feishu: initialized %d bots", len(p.bots))
	log.Printf("feishu: tools (e.g. feishuSendFile, feishuSendText) are registered when DMR first collects tools for an agent run (ProvideTools RPC)")

	p.scheduleInboundRetentionCleanup()

	return nil
}

func (p *FeishuPlugin) Shutdown(req *proto.ShutdownRequest, resp *proto.ShutdownResponse) error {
	p.shutdown.Do(func() {
		p.runMu.Lock()
		if p.cancel != nil {
			p.cancel()
			p.cancel = nil
		}
		p.runMu.Unlock()
		if p.queues != nil {
			p.queues.shutdownAll()
		}
		p.botsMu.Lock()
		for _, bot := range p.bots {
			bot.wsClient = nil
		}
		p.bots = nil
		p.botsMu.Unlock()
	})
	return nil
}

func (p *FeishuPlugin) RequestApproval(req *proto.ApprovalRequest, resp *proto.ApprovalResult) error {
	chatID, ok := p2pChatIDFromTape(strings.TrimSpace(req.Tape))
	if !ok {
		resp.Choice = choiceDenied
		resp.Comment = "unknown tape routing for approval"
		return nil
	}
	bot, err := p.getBotForChat(chatID)
	if err != nil {
		resp.Choice = choiceDenied
		resp.Comment = "no bot found for chat"
		return nil
	}
	if bot.approver == nil {
		resp.Choice = choiceDenied
		return nil
	}
	bot.approver.handleSingle(req, resp)
	return nil
}

func (p *FeishuPlugin) RequestBatchApproval(req *proto.BatchApprovalRequest, resp *proto.BatchApprovalResult) error {
	if len(req.Requests) == 0 {
		resp.Choice = choiceDenied
		return nil
	}
	chatID, ok := p2pChatIDFromTape(strings.TrimSpace(req.Requests[0].Tape))
	if !ok {
		resp.Choice = choiceDenied
		return nil
	}
	bot, err := p.getBotForChat(chatID)
	if err != nil {
		resp.Choice = choiceDenied
		return nil
	}
	if bot.approver == nil {
		resp.Choice = choiceDenied
		return nil
	}
	bot.approver.handleBatch(req, resp)
	return nil
}

func (p *FeishuPlugin) ProvideTools(req *proto.ProvideToolsRequest, resp *proto.ProvideToolsResponse) error {
	resp.Tools = []proto.ToolDef{
		{
			Name:        "feishuSendFile",
			Description: "Required way to deliver reports in Feishu: upload a local file and send it as a file message in the current p2p chat. Only when this run was triggered from Feishu. Required: path to an existing file (write report body with fsWrite first). Path resolves under send_file_root if set, else process cwd; must not escape that root. Optional filename overrides the upload name; optional caption sends a short text line first. Max size: send_file_max_bytes (default 30 MiB).",
			ParametersJSON: sendFileToolParamsJSON(),
		},
		{
			Name:        "feishuSendText",
			Description: "Send a short non-report text (or Markdown post) to the current Feishu p2p chat. Do not use for report/analysis/summary body—use feishuSendFile after writing a file. When the run was triggered from Feishu, omit tape_name/chat_id. For runs without inbound context (e.g. cron on feishu:p2p tape), set tape_name (feishu:p2p:<chat_id>) or chat_id. Optional markdown=true. Never set tape_name/chat_id together with a Feishu-triggered job.",
			ParametersJSON: sendTextToolParamsJSON(),
		},
	}
	names := make([]string, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		names = append(names, t.Name)
	}
	log.Printf("feishu: ProvideTools -> %s", strings.Join(names, ", "))
	return nil
}

func (p *FeishuPlugin) CallTool(req *proto.CallToolRequest, resp *proto.CallToolResponse) error {
	ctx := context.Background()
	p.runMu.Lock()
	if p.runCtx != nil {
		ctx = p.runCtx
	}
	p.runMu.Unlock()

	switch req.Name {
	case "feishuSendFile":
		result, err := p.execSendFile(ctx, req.ArgsJSON)
		if err != nil {
			resp.Error = err.Error()
			return nil
		}
		b, err := json.Marshal(result)
		if err != nil {
			resp.Error = err.Error()
			return nil
		}
		resp.ResultJSON = string(b)
		return nil
	case "feishuSendText":
		result, err := p.execSendText(ctx, req.ArgsJSON)
		if err != nil {
			resp.Error = err.Error()
			return nil
		}
		b, err := json.Marshal(result)
		if err != nil {
			resp.Error = err.Error()
			return nil
		}
		resp.ResultJSON = string(b)
		return nil
	default:
		resp.Error = fmt.Sprintf("unknown tool: %s", req.Name)
		return nil
	}
}
