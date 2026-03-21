package main

import (
	"context"
	"log"
	"strings"
	"sync"
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
			out = "(no output)"
		}
		_ = p.replyAgentOutput(ctx, job, out)
	}
}
