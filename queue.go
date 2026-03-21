package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// inboundJob is one user message to process serially on the global Feishu queue.
type inboundJob struct {
	QueueKey string // same as TapeName; kept for logs
	TapeName string

	ChatID           string
	SenderID         string
	Content          string
	TriggerMessageID string
	ChatType         string
	ThreadKey        string
	InThread         bool
}

type queueManager struct {
	plugin        *FeishuPlugin
	mu            sync.Mutex
	jobs          chan *inboundJob
	workerStarted bool
}

func newQueueManager(p *FeishuPlugin) *queueManager {
	return &queueManager{
		plugin: p,
		jobs:   make(chan *inboundJob, 64),
	}
}

func (qm *queueManager) enqueue(job *inboundJob) {
	if job == nil || job.TapeName == "" {
		return
	}
	qm.mu.Lock()
	if qm.jobs == nil {
		qm.mu.Unlock()
		return
	}
	if !qm.workerStarted {
		qm.workerStarted = true
		go qm.plugin.runWorker(qm.jobs)
	}
	ch := qm.jobs
	qm.mu.Unlock()
	ch <- job
}

func (qm *queueManager) shutdown() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	if qm.jobs != nil {
		close(qm.jobs)
		qm.jobs = nil
		qm.workerStarted = false
	}
}

func (p *FeishuPlugin) runWorker(jobs <-chan *inboundJob) {
	for job := range jobs {
		if job != nil {
			log.Printf("feishu: worker job tape=%q chatID=%q inThread=%v msgID=%q preview=%q",
				job.TapeName, job.ChatID, job.InThread, job.TriggerMessageID, job.Content)
		}
		p.processJob(job)
	}
}

func (p *FeishuPlugin) processJob(job *inboundJob) {
	if job == nil {
		return
	}
	ctx := context.Background()
	log.Printf("feishu: processJob tape=%q", job.TapeName)

	p.setActiveJob(job)
	defer p.clearActiveJob()

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
// (e.g. after feishu.send_file). RunAgent still succeeds with Output==""; avoid a bare "(no output)".
func feishuFallbackWhenNoText(tape string, resp *proto.RunAgentResponse) string {
	if resp == nil {
		return "(no output)"
	}
	if len(resp.ToolCalls) == 0 {
		if resp.Steps > 0 {
			return fmt.Sprintf(
				"助手未返回可见文字（模型最后一轮可能为空）。本轮约 %d 步；请查 DMR tape「%s」或主机日志。若应交付报告，请确认已调用 feishu.send_file。",
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
			"若包含 feishu.send_file，请在本对话中查看文件消息；其余请看工具返回或 tape。",
		strings.Join(shown, ", "),
		ellipsis,
	)
}
