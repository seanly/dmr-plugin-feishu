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
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// FeishuPlugin implements proto.DMRPluginInterface and proto.HostClientSetter.
type FeishuPlugin struct {
	cfg FeishuConfig

	lc       *lark.Client
	wsClient *larkws.Client

	hostMu     sync.Mutex
	hostClient *rpc.Client

	runMu    sync.Mutex
	runCtx   context.Context
	cancel   context.CancelFunc
	shutdown sync.Once

	dedup    *deduper
	approver *FeishuApprover
	queues   *queueManager

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
		cfg: defaultFeishuConfig(),
	}
	p.approver = newFeishuApprover(p)
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

	if cfg.AppID == "" || cfg.AppSecret == "" {
		return fmt.Errorf("feishu: app_id and app_secret are required")
	}
	if strings.TrimSpace(cfg.DmrRestartToken) != "" && len(cfg.AllowFrom) == 0 {
		return fmt.Errorf("feishu: dmr_restart_token requires allow_from (restrict who can restart DMR)")
	}

	resolvedExtra, err := buildResolvedExtraPrompt(cfg)
	if err != nil {
		return fmt.Errorf("feishu: %w", err)
	}
	p.extraRunPrompt = resolvedExtra
	if resolvedExtra != "" {
		log.Printf("feishu: extra run prompt enabled (%d bytes); prepended to inbound RunAgent user message", len(resolvedExtra))
	}

	// Debug-only sanity check: verify config was unmarshaled correctly.
	// We intentionally do not print secrets.
	vtSet := strings.TrimSpace(cfg.VerificationToken) != ""
	ekSet := strings.TrimSpace(cfg.EncryptKey) != ""
	appIDPrefix := cfg.AppID
	if len(appIDPrefix) > 6 {
		appIDPrefix = appIDPrefix[:6]
	}
	log.Printf("feishu: init cfg vt_set=%v ek_set=%v allow_from=%d app_id_prefix=%q (p2p-only)",
		vtSet, ekSet, len(cfg.AllowFrom), appIDPrefix)
	log.Printf("feishu: tools (e.g. feishuSendFile, feishuSendText) are registered when DMR first collects tools for an agent run (ProvideTools RPC), not during this Init")

	p.dedup = newDeduper(cfg.dedupTTL())
	p.lc = lark.NewClient(cfg.AppID, cfg.AppSecret)

	p.runMu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.runCtx = ctx
	p.cancel = cancel
	p.runMu.Unlock()

	dispatcher := larkdispatcher.NewEventDispatcher(cfg.VerificationToken, cfg.EncryptKey).
		OnP2MessageReceiveV1(p.handleMessageReceive)

	p.wsClient = larkws.NewClient(cfg.AppID, cfg.AppSecret, larkws.WithEventHandler(dispatcher))
	ws := p.wsClient

	go func() {
		log.Printf("feishu: websocket client starting")
		if err := ws.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("feishu: websocket stopped: %v", err)
		}
	}()

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
			p.queues.shutdown()
		}
		p.wsClient = nil
	})
	return nil
}

func (p *FeishuPlugin) RequestApproval(req *proto.ApprovalRequest, resp *proto.ApprovalResult) error {
	if p.approver == nil {
		resp.Choice = choiceDenied
		return nil
	}
	p.approver.handleSingle(req, resp)
	return nil
}

func (p *FeishuPlugin) RequestBatchApproval(req *proto.BatchApprovalRequest, resp *proto.BatchApprovalResult) error {
	if p.approver == nil {
		resp.Choice = choiceDenied
		return nil
	}
	p.approver.handleBatch(req, resp)
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
