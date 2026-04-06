package queue

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/seanly/dmr/pkg/plugin/proto"
)



// Job is one user message to process serially per chat_id.
type Job struct {
	ID               string      // Unique job ID for tracking
	QueueKey         string
	TapeName         string
	ChatID           string
	Bot              interface{} // *bot.Instance - use interface to avoid import cycle
	SenderID         string
	Content          string
	TriggerMessageID string
	ChatType         string
	ThreadKey        string
	InThread         bool
}

// Handler is the callback to process a job.
type Handler interface {
	ProcessJob(job *Job)
	ComposeRunPrompt(userContent string) string
	// CallRunAgentWithContext calls RunAgent with plugin context.
	// The context map is passed to tool calls, allowing them to access trigger-time information.
	CallRunAgentWithContext(tape, prompt string, historyAfter int64, ctx map[string]any) (*proto.RunAgentResponse, error)
	ReplyAgentOutputWithContext(ctx context.Context, chatID, triggerMessageID string, inThread bool, output string) error
}

// Manager manages per-chat job queues.
type Manager struct {
	handler Handler

	mu      sync.Mutex
	workers map[string]chan *Job // chat_id -> per-chat job channel
	closed  bool
	wg      sync.WaitGroup
}

// NewManager creates a new queue manager.
func NewManager(handler Handler) *Manager {
	return &Manager{
		handler: handler,
		workers: make(map[string]chan *Job),
	}
}

// Enqueue adds a job to the appropriate queue.
func (qm *Manager) Enqueue(job *Job) {
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
		ch = make(chan *Job, 16)
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

func (qm *Manager) runWorkerForChat(chatID string, jobs <-chan *Job) {
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
				qm.handler.ProcessJob(job)
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

// ShutdownAll stops all queue workers.
func (qm *Manager) ShutdownAll() {
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

// ProcessJob wraps the handler's process job with standard logic.
// Uses context passing instead of maintaining in-memory active job state.
func ProcessJob(ctx context.Context, handler Handler, job *Job) {
	if job == nil {
		return
	}
	
	// Assign unique job ID for tracking
	if job.ID == "" {
		job.ID = uuid.New().String()
	}
	
	log.Printf("feishu: processJob tape=%q job=%s", job.TapeName, job.ID[:8])

	// Comma commands (command plugin InterceptInput) require the prompt to start with "," after trim.
	userTrim := strings.TrimSpace(job.Content)
	runPrompt := handler.ComposeRunPrompt(job.Content)
	if strings.HasPrefix(userTrim, ",") || strings.HasPrefix(userTrim, "，") {
		if strings.HasPrefix(userTrim, "，") {
			userTrim = "," + strings.TrimPrefix(userTrim, "，")
		}
		runPrompt = userTrim
	}
	
	// Build context to pass to RunAgent - this replaces the active job mechanism
	// Tools will receive this context and can extract chat_id, message_id, etc.
	jobCtx := map[string]any{
		"chat_id":             job.ChatID,
		"trigger_message_id":  job.TriggerMessageID,
		"in_thread":           job.InThread,
		"sender_id":           job.SenderID,
		"chat_type":           job.ChatType,
		"thread_key":          job.ThreadKey,
	}
	
	// Call RunAgent with context - no need for active job management
	resp, err := handler.CallRunAgentWithContext(job.TapeName, runPrompt, 0, jobCtx)
	if err != nil {
		log.Printf("feishu: RunAgent RPC error: %v", err)
		_ = handler.ReplyAgentOutputWithContext(ctx, job.ChatID, job.TriggerMessageID, job.InThread, "DMR: RunAgent failed: "+err.Error())
		return
	}
	if resp == nil {
		return
	}
	if resp.Error != "" {
		_ = handler.ReplyAgentOutputWithContext(ctx, job.ChatID, job.TriggerMessageID, job.InThread, "DMR error: "+resp.Error)
	} else {
		out := resp.Output
		if out == "" {
			out = FallbackWhenNoText(job.TapeName, resp)
			log.Printf("feishu: RunAgent empty output tape=%q steps=%d toolCalls=%d", job.TapeName, resp.Steps, len(resp.ToolCalls))
		}
		_ = handler.ReplyAgentOutputWithContext(ctx, job.ChatID, job.TriggerMessageID, job.InThread, out)
	}
}

// FallbackWhenNoText explains empty agent Output.
func FallbackWhenNoText(tape string, resp *proto.RunAgentResponse) string {
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
