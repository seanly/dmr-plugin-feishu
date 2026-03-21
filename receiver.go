package main

import (
	"context"
	"log"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func (p *FeishuPlugin) handleMessageReceive(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
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

	senderID := extractFeishuSenderID(sender)
	if senderID == "" {
		senderID = "unknown"
	}

	content := extractFeishuMessageContent(message)
	if content == "" {
		content = "[empty message]"
	}

	msgID := stringValue(message.MessageId)
	if p.dedup != nil && p.dedup.isDuplicate(msgID) {
		log.Printf("feishu: dedup skip msgID=%q chatID=%q senderID=%q", msgID, chatID, senderID)
		return nil
	}

	chatType := stringValue(message.ChatType)
	threadKey := stringValue(message.ThreadId)
	inThread := threadKey != ""

	log.Printf(
		"feishu: message received chatType=%q chatID=%q senderID=%q msgID=%q threadId=%q inThread=%v preview=%q",
		chatType, chatID, senderID, msgID, threadKey, inThread, content,
	)

	if chatType != "p2p" {
		log.Printf("feishu: ignoring non-p2p message (plugin is private-chat only)")
		return nil
	}

	// Approval replies must not start the agent.
	if p.approver != nil && p.approver.TryResolveP2P(chatID, content) {
		log.Printf("feishu: p2p approval reply consumed (chatID=%q)", chatID)
		return nil
	}

	senderOK := isAllowedSender(p.cfg.AllowFrom, senderID)
	if !senderOK {
		log.Printf("feishu: sender not allowed (senderID=%q allow_from=%v)", senderID, p.cfg.AllowFrom)
		return nil
	}

	tape := tapeNameForP2P(chatID)
	job := &inboundJob{
		QueueKey:         tape,
		TapeName:         tape,
		ChatID:           chatID,
		SenderID:         senderID,
		Content:          content,
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
