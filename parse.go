package main

import (
	"encoding/json"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func extractFeishuSenderID(sender *larkim.EventSender) string {
	if sender == nil || sender.SenderId == nil {
		return ""
	}
	if sender.SenderId.UserId != nil && *sender.SenderId.UserId != "" {
		return *sender.SenderId.UserId
	}
	if sender.SenderId.OpenId != nil && *sender.SenderId.OpenId != "" {
		return *sender.SenderId.OpenId
	}
	if sender.SenderId.UnionId != nil && *sender.SenderId.UnionId != "" {
		return *sender.SenderId.UnionId
	}
	return ""
}

func extractFeishuMessageContent(message *larkim.EventMessage) string {
	if message == nil || message.Content == nil || *message.Content == "" {
		return ""
	}
	if message.MessageType != nil && *message.MessageType == larkim.MsgTypeText {
		var textPayload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(*message.Content), &textPayload); err == nil {
			return textPayload.Text
		}
	}
	return *message.Content
}

// tapeNameForP2P builds the DMR tape name for private chat (p2p-only plugin).
func tapeNameForP2P(chatID string) string {
	return "feishu:p2p:" + chatID
}

func p2pChatIDFromTape(tape string) (chatID string, ok bool) {
	const p = "feishu:p2p:"
	if !strings.HasPrefix(tape, p) {
		return "", false
	}
	return tape[len(p):], true
}
