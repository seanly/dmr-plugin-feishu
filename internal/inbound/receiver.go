package inbound

import (
	"context"
	"fmt"
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

// ShouldSkipRunAgentStandaloneMediaGroup is true for group image/file messages with no text.
// In groups, standalone media without @mention should be ignored.
func ShouldSkipRunAgentStandaloneMediaGroup(message *larkim.EventMessage) bool {
	if message == nil {
		return false
	}
	mt := StringValue(message.MessageType)
	if mt != larkim.MsgTypeImage && mt != larkim.MsgTypeFile {
		return false
	}
	// If there's a parent_id (reply context), don't skip
	if strings.TrimSpace(StringValue(message.ParentId)) != "" {
		return false
	}
	return true
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
		SendTextReply(chatID, text string) error
		// Group chat support
		IsGroupEnabledForBot(botInst interface{}) bool
		GetBotID(botInst interface{}) string
		GetApproverOpenID(botInst interface{}) string
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

	// Determine chat type
	chatType := GetChatType(message)
	threadKey := StringValue(message.ThreadId)
	inThread := threadKey != ""

	// Route to appropriate handler
	if IsP2PChat(message) {
		return r.handleP2PMessage(ctx, botInst, larkClient, allowFrom, event, chatID, senderID, msgID, threadKey, inThread)
	}

	if IsGroupChat(message) {
		return r.handleGroupMessage(ctx, botInst, larkClient, allowFrom, event, chatID, senderID, msgID, threadKey, inThread)
	}

	log.Printf("feishu: unknown chat type (chatType=%q), ignoring", chatType)
	return nil
}

// handleP2PMessage handles private chat messages.
func (r *Receiver) handleP2PMessage(ctx context.Context, botInst interface{}, larkClient *lark.Client, allowFrom []string, event *larkim.P2MessageReceiveV1, chatID, senderID, msgID, threadKey string, inThread bool) error {
	message := event.Event.Message

	// Build user content
	userText := r.Plugin.BuildInboundUserContent(ctx, larkClient, message, msgID)

	modelContent := r.Plugin.MergeInboundReplyContext(ctx, larkClient, message, userText)
	modelPreview := modelContent
	if modelPreview != TruncateReplyContextBody(modelPreview, 200) {
		modelPreview = TruncateReplyContextBody(modelPreview, 200) + "…"
	}
	log.Printf("feishu: p2p message received chatID=%q senderID=%q msgID=%q modelPreview=%q",
		chatID, senderID, msgID, modelPreview)

	// Special command: reply user's open_id and bot's open_id (only in p2p)
	trimmed := strings.TrimSpace(userText)
	if trimmed == ",id" || trimmed == ",openid" {
		botID := r.Plugin.GetBotID(botInst)
		replyText := fmt.Sprintf("机器人 Open ID:\n`%s`\n\n你的 Open ID:\n`%s`\n\nChat ID:\n`%s`", botID, senderID, chatID)
		r.Plugin.SendTextReply(chatID, replyText)
		return nil
	}

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
		ChatType:         "p2p",
		ThreadKey:        threadKey,
		InThread:         inThread,
	}

	log.Printf("feishu: enqueue p2p job tape=%q chatID=%q", tape, chatID)
	r.Plugin.EnqueueJob(job)
	return nil
}

// handleGroupMessage handles group chat messages.
func (r *Receiver) handleGroupMessage(ctx context.Context, botInst interface{}, larkClient *lark.Client, allowFrom []string, event *larkim.P2MessageReceiveV1, chatID, senderID, msgID, threadKey string, inThread bool) error {
	// 0. Check if this bot has group chat enabled
	if !r.Plugin.IsGroupEnabledForBot(botInst) {
		log.Printf("feishu: group message ignored (bot group_enabled=false)")
		return nil
	}

	message := event.Event.Message

	// 1. Check if bot is mentioned
	botID := r.Plugin.GetBotID(botInst)
	mentionInfo := CheckMention(message, botID)

	// Not mentioned at all - ignore
	if mentionInfo.Type == MentionTypeNone {
		log.Printf("feishu: group message ignored (bot not mentioned)")
		return nil
	}

	// @all - ignore (hardcoded behavior)
	if mentionInfo.Type == MentionTypeAtAll {
		log.Printf("feishu: group message ignored (@all)")
		return nil
	}

	// 2. Build user content (strip @mentions from text)
	userText := r.Plugin.BuildInboundUserContent(ctx, larkClient, message, msgID)

	// Strip @bot mentions from the text content
	cleanText := StripMentionText(userText, message)

	modelContent := r.Plugin.MergeInboundReplyContext(ctx, larkClient, message, cleanText)
	modelPreview := modelContent
	if modelPreview != TruncateReplyContextBody(modelPreview, 200) {
		modelPreview = TruncateReplyContextBody(modelPreview, 200) + "…"
	}
	log.Printf("feishu: group message received chatID=%q threadID=%q senderID=%q msgID=%q modelPreview=%q",
		chatID, threadKey, senderID, msgID, modelPreview)

	// 3. Skip standalone media without meaningful text
	if ShouldSkipRunAgentStandaloneMediaGroup(message) {
		log.Printf("feishu: standalone %s in group save-only (no RunAgent)", StringValue(message.MessageType))
		return nil
	}

	// 4. Create job with group tape naming
	tape := TapeNameForGroup(chatID, threadKey)
	queueKey := QueueKeyForGroup(chatID, threadKey)

	job := &Job{
		QueueKey:         queueKey,
		TapeName:         tape,
		ChatID:           chatID,
		Bot:              botInst,
		SenderID:         senderID,
		Content:          modelContent,
		TriggerMessageID: msgID,
		ChatType:         "group",
		ThreadKey:        threadKey,
		InThread:         inThread,
	}

	log.Printf("feishu: enqueue group job tape=%q queue=%q chatID=%q", tape, queueKey, chatID)
	r.Plugin.EnqueueJob(job)
	return nil
}
