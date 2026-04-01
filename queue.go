package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// inboundJob is one user message to process serially per chat_id.
type inboundJob struct {
	QueueKey string // same as TapeName; kept for logs
	TapeName string

	ChatID           string
	Bot              *BotInstance // the bot instance that received this message
	SenderID         string
	Content          string
	TriggerMessageID string
	ChatType         string
	ThreadKey        string
	InThread         bool
}

type queueManager struct {
	plugin *FeishuPlugin

	mu      sync.Mutex
	workers map[string]chan *inboundJob // chat_id -> per-chat job channel
	closed  bool
	wg      sync.WaitGroup
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
	if qm.closed {
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
				log.Printf("feishu: worker processing chat_id=%q tape=%q msgID=%q",
					chatID, job.TapeName, job.TriggerMessageID)
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
	if qm.closed {
		qm.mu.Unlock()
		return
	}
	qm.closed = true
	for chatID, ch := range qm.workers {
		close(ch)
		log.Printf("feishu: closing queue worker for chat_id=%q", chatID)
	}
	qm.mu.Unlock()
	qm.wg.Wait()
	log.Printf("feishu: all queue workers stopped")
}

func (p *FeishuPlugin) processJob(job *inboundJob) {
	if job == nil {
		return
	}
	ctx := context.Background()
	log.Printf("feishu: processJob tape=%q", job.TapeName)

	p.setActiveJob(job)
	defer p.clearActiveJob(job.TapeName)

	// HistoryAfterEntryID 0 => DMR uses default tape read (LastAnchorContext); no plugin-side TapeHandoff.
	// Comma commands (command plugin InterceptInput) require the prompt to start with "," after trim.
	// composeRunPrompt prefixes Feishu hints, which would hide a leading ",help" etc. from intercept.
	userTrim := strings.TrimSpace(job.Content)
	runPrompt := p.composeRunPrompt(job.Content)
	if strings.HasPrefix(userTrim, ",") || strings.HasPrefix(userTrim, "，") {
		if strings.HasPrefix(userTrim, "，") {
			userTrim = "," + strings.TrimPrefix(userTrim, "，")
		}
		runPrompt = userTrim
	}
	resp, err := p.callRunAgent(job.TapeName, runPrompt, 0)
	if err != nil {
		log.Printf("feishu: RunAgent RPC error: %v", err)
		_ = p.replyAgentOutput(ctx, job, "DMR: RunAgent failed: "+err.Error())
		return
	}
	if resp == nil {
		return
	}
	if resp.Error != "" {
		_ = p.replyAgentOutput(ctx, job, "DMR error: "+resp.Error)
	} else {
		out := resp.Output
		if out == "" {
			out = feishuFallbackWhenNoText(job.TapeName, resp)
			log.Printf("feishu: RunAgent empty output tape=%q steps=%d toolCalls=%d", job.TapeName, resp.Steps, len(resp.ToolCalls))
		}
		_ = p.replyAgentOutput(ctx, job, out)
	}
}

// feishuFallbackWhenNoText explains empty agent Output: models sometimes finish with tool calls only
// (e.g. after feishuSendFile). RunAgent still succeeds with Output==""; avoid a bare "(no output)".
func feishuFallbackWhenNoText(tape string, resp *proto.RunAgentResponse) string {
	if resp == nil {
		return "(no output)"
	}
	if len(resp.ToolCalls) == 0 {
		if resp.Steps > 0 {
			return fmt.Sprintf(
				"助手未返回可见文字（模型最后一轮可能为空）。本轮约 %d 步；请查 DMR tape「%s」或主机日志。若应交付报告，请确认已调用 feishuSendFile。",
				resp.Steps, strings.TrimSpace(tape),
			)
		}
		return "未产生助手回复（0 步）。请检查模型/API 是否报错或重试。"
	}
	var names []string
	for _, tc := range resp.ToolCalls {
		n := strings.TrimSpace(tc.Name)
		if n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		if resp.Steps > 0 {
			return fmt.Sprintf("助手未返回文字；已记录 %d 步工具调用但无名称摘要，请查 tape。", resp.Steps)
		}
		return "(no output)"
	}
	const maxShow = 12
	shown := names
	ellipsis := ""
	if len(names) > maxShow {
		shown = names[:maxShow]
		ellipsis = fmt.Sprintf(" …（共 %d 次工具调用）", len(names))
	} else {
		ellipsis = fmt.Sprintf("（共 %d 次）", len(names))
	}
	return fmt.Sprintf(
		"本轮助手未输出文字，但已执行：%s%s。\n"+
			"若包含 feishuSendFile，请在本对话中查看文件消息；其余请看工具返回或 tape。",
		strings.Join(shown, ", "),
		ellipsis,
	)
}
