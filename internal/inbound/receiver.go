package inbound

import (
	"context"
	"log"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// ShouldSkipRunAgentStandaloneMedia is true for p2p image/file messages with no parent_id.
func ShouldSkipRunAgentStandaloneMedia(message *larkim.EventMessage) bool {
	if message == nil {
		return false
	}
	if strings.TrimSpace(StringValue(message.ParentId)) != "" {
		return false
	}
	mt := StringValue(message.MessageType)
	return mt == larkim.MsgTypeImage || mt == larkim.MsgTypeFile
}

// Job represents an inbound job.
type Job struct {
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

// Receiver handles incoming Feishu messages.
type Receiver struct {
	Plugin interface {
		RegisterChatRoute(chatID string, bot interface{})
		GetDeduper() *Deduper
		BuildInboundUserContent(ctx context.Context, client *lark.Client, message *larkim.EventMessage, msgID string) string
		MergeInboundReplyContext(ctx context.Context, client *lark.Client, message *larkim.EventMessage, userText string) string
		TryResolveApproval(chatID, content string) bool
		IsAllowedSender(allowList []string, senderID string) bool
		EnqueueJob(job *Job)
	}
}

// HandleMessageReceive processes an incoming message.
func (r *Receiver) HandleMessageReceive(ctx context.Context, botInst interface{}, larkClient *lark.Client, allowFrom []string, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		log.Printf("feishu: P2MessageReceiveV1: nil event/event.message")
		return nil
	}
	message := event.Event.Message
	sender := event.Event.Sender

	chatID := StringValue(message.ChatId)
	if chatID == "" {
		log.Printf("feishu: message.ChatId empty")
		return nil
	}

	// Register chat_id -> bot routing
	r.Plugin.RegisterChatRoute(chatID, botInst)

	senderID := ExtractFeishuSenderID(sender)
	if senderID == "" {
		senderID = "unknown"
	}

	msgID := StringValue(message.MessageId)
	if deduper := r.Plugin.GetDeduper(); deduper != nil && deduper.IsDuplicate(msgID) {
		log.Printf("feishu: dedup skip msgID=%q chatID=%q senderID=%q", msgID, chatID, senderID)
		return nil
	}

	// Build user content
	userText := r.Plugin.BuildInboundUserContent(ctx, larkClient, message, msgID)

	chatType := StringValue(message.ChatType)
	threadKey := StringValue(message.ThreadId)
	inThread := threadKey != ""

	if chatType != "p2p" {
		log.Printf("feishu: ignoring non-p2p message (chatType=%q)", chatType)
		return nil
	}

	modelContent := r.Plugin.MergeInboundReplyContext(ctx, larkClient, message, userText)
	modelPreview := modelContent
	if modelPreview != TruncateReplyContextBody(modelPreview, 200) {
		modelPreview = TruncateReplyContextBody(modelPreview, 200) + "…"
	}
	log.Printf("feishu: message received chatType=%q chatID=%q senderID=%q msgID=%q modelPreview=%q",
		chatType, chatID, senderID, msgID, modelPreview)

	// Approval replies must not start the agent
	if r.Plugin.TryResolveApproval(chatID, userText) {
		log.Printf("feishu: p2p approval reply consumed (chatID=%q)", chatID)
		return nil
	}

	// Use bot-specific allow_from
	if !r.Plugin.IsAllowedSender(allowFrom, senderID) {
		log.Printf("feishu: sender not allowed (senderID=%q)", senderID)
		return nil
	}

	if ShouldSkipRunAgentStandaloneMedia(message) {
		log.Printf("feishu: standalone %s inbound save-only (no RunAgent)", StringValue(message.MessageType))
		return nil
	}

	tape := TapeNameForP2P(chatID)
	job := &Job{
		QueueKey:         tape,
		TapeName:         tape,
		ChatID:           chatID,
		Bot:              botInst,
		SenderID:         senderID,
		Content:          modelContent,
		TriggerMessageID: msgID,
		ChatType:         chatType,
		ThreadKey:        threadKey,
		InThread:         inThread,
	}

	log.Printf("feishu: enqueue job tape=%q chatID=%q", tape, chatID)
	r.Plugin.EnqueueJob(job)
	return nil
}
