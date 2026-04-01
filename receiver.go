package main

import (
	"context"
	"log"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// shouldSkipRunAgentStandaloneMedia is true for p2p image/file messages with no parent_id.
// buildInboundUserContent still runs first (download + disk when inbound_media_enabled), but we do not enqueue RunAgent
// until the user references the message in a reply (parent_id set on a later event).
func shouldSkipRunAgentStandaloneMedia(message *larkim.EventMessage) bool {
	if message == nil {
		return false
	}
	if strings.TrimSpace(stringValue(message.ParentId)) != "" {
		return false
	}
	mt := stringValue(message.MessageType)
	return mt == larkim.MsgTypeImage || mt == larkim.MsgTypeFile
}

func (p *FeishuPlugin) handleMessageReceive(ctx context.Context, bot *BotInstance, event *larkim.P2MessageReceiveV1) error {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		log.Printf("feishu: P2MessageReceiveV1: nil event/event.message (event=%T)", event)
		return nil
	}
	message := event.Event.Message
	sender := event.Event.Sender

	chatID := stringValue(message.ChatId)
	if chatID == "" {
		log.Printf("feishu: message.ChatId empty (chatType=%q threadId=%q msgId=%q)",
			stringValue(message.ChatType), stringValue(message.ThreadId), stringValue(message.MessageId))
		return nil
	}

	// Register chat_id -> bot routing.
	p.registerChatRoute(chatID, bot)

	senderID := extractFeishuSenderID(sender)
	if senderID == "" {
		senderID = "unknown"
	}

	msgID := stringValue(message.MessageId)
	if p.dedup != nil && p.dedup.isDuplicate(msgID) {
		log.Printf("feishu: dedup skip msgID=%q chatID=%q senderID=%q", msgID, chatID, senderID)
		return nil
	}

	userText := p.buildInboundUserContent(ctx, bot, message)

	chatType := stringValue(message.ChatType)
	threadKey := stringValue(message.ThreadId)
	inThread := threadKey != ""

	if chatType != "p2p" {
		log.Printf(
			"feishu: message received chatType=%q chatID=%q senderID=%q msgID=%q parentId=%q threadId=%q inThread=%v userPreview=%q",
			chatType, chatID, senderID, msgID, stringValue(message.ParentId), threadKey, inThread, userText,
		)
		log.Printf("feishu: ignoring non-p2p message (plugin is private-chat only)")
		return nil
	}

	modelContent := p.mergeInboundReplyContext(ctx, bot, message, userText)
	modelPreview := modelContent
	// truncateRunes is byte-safe for UTF-8; cap log line by runes like outbound helpers.
	if modelPreview != truncateRunes(modelPreview, 200) {
		modelPreview = truncateRunes(modelPreview, 200) + "…"
	}
	log.Printf(
		"feishu: message received chatType=%q chatID=%q senderID=%q msgID=%q parentId=%q threadId=%q inThread=%v userPreview=%q modelPreview=%q",
		chatType, chatID, senderID, msgID, stringValue(message.ParentId), threadKey, inThread, userText, modelPreview,
	)

	// Approval replies must not start the agent (match on user-typed text only, not quoted parent block).
	if bot.approver != nil && bot.approver.TryResolveP2P(chatID, userText) {
		log.Printf("feishu: p2p approval reply consumed (chatID=%q)", chatID)
		return nil
	}

	// Use bot-specific allow_from.
	senderOK := isAllowedSender(bot.cfg.AllowFrom, senderID)
	if !senderOK {
		log.Printf("feishu: sender not allowed (senderID=%q allow_from=%v)", senderID, bot.cfg.AllowFrom)
		return nil
	}

	if p.tryHandleDMRRestart(ctx, bot, chatID, msgID, inThread, userText) {
		return nil
	}

	if shouldSkipRunAgentStandaloneMedia(message) {
		log.Printf("feishu: standalone %s inbound save-only (no RunAgent); msgID=%q chatID=%q", stringValue(message.MessageType), msgID, chatID)
		return nil
	}

	tape := tapeNameForP2P(chatID)
	job := &inboundJob{
		QueueKey:         tape,
		TapeName:         tape,
		ChatID:           chatID,
		Bot:              bot,
		SenderID:         senderID,
		Content:          modelContent,
		TriggerMessageID: msgID,
		ChatType:         chatType,
		ThreadKey:        threadKey,
		InThread:         inThread,
	}

	if p.queues != nil {
		log.Printf("feishu: enqueue job tape=%q chatID=%q inThread=%v triggerMessageID=%q", tape, chatID, inThread, msgID)
		p.queues.enqueue(job)
	}
	return nil
}
