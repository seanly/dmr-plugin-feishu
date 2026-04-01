package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	quotedBlockStart = "<<< feishu_quoted_message"
	quotedBlockEnd   = ">>> end_feishu_quoted_message"
)

// replyContextIDs holds Feishu message ids for the quoted block header.
type replyContextIDs struct {
	ParentMessageID  string
	CurrentMessageID string
	RootMessageID    string
}

// larkMessageToEventMessage maps API Message (message/get) into EventMessage shape for parse/buildInboundUserContent.
func larkMessageToEventMessage(m *larkim.Message) *larkim.EventMessage {
	if m == nil {
		return nil
	}
	out := &larkim.EventMessage{
		MessageId:   m.MessageId,
		RootId:      m.RootId,
		ParentId:    m.ParentId,
		ThreadId:    m.ThreadId,
		MessageType: m.MsgType,
	}
	if m.Body != nil && m.Body.Content != nil {
		c := *m.Body.Content
		out.Content = &c
	}
	return out
}

func messageFromGetRespData(data *larkim.GetMessageRespData) *larkim.Message {
	if data == nil {
		return nil
	}
	if len(data.Items) > 0 && data.Items[0] != nil {
		return data.Items[0]
	}
	return nil
}

func (p *FeishuPlugin) fetchParentMessage(ctx context.Context, bot *BotInstance, parentMessageID string) (*larkim.Message, error) {
	if bot.lc == nil {
		return nil, fmt.Errorf("feishu client not initialized")
	}
	parentMessageID = strings.TrimSpace(parentMessageID)
	if parentMessageID == "" {
		return nil, fmt.Errorf("empty parent_message_id")
	}
	dctx, cancel := context.WithTimeout(ctx, p.cfg.replyContextTimeout())
	defer cancel()

	req := larkim.NewGetMessageReqBuilder().
		MessageId(parentMessageID).
		UserIdType(larkim.UserIdTypeGetMessageOpenId).
		Build()
	resp, err := bot.lc.Im.V1.Message.Get(dctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil || !resp.Success() {
		code, msg := 0, ""
		if resp != nil {
			code, msg = resp.Code, resp.Msg
		}
		return nil, fmt.Errorf("feishu message.get failed code=%d msg=%s", code, msg)
	}
	m := messageFromGetRespData(resp.Data)
	if m == nil {
		return nil, fmt.Errorf("feishu message.get: empty data.items")
	}
	if m.Deleted != nil && *m.Deleted {
		return nil, errQuotedMessageDeleted
	}
	return m, nil
}

var errQuotedMessageDeleted = errors.New("feishu: quoted parent message was deleted")

func isCommaCommandMessage(userText string) bool {
	s := strings.TrimSpace(userText)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, ",") {
		return true
	}
	r, sz := utf8.DecodeRuneInString(s)
	if sz > 0 && r == '，' {
		return true
	}
	return false
}

// truncateReplyContextBody caps parent body by runes and appends a truncation line if needed.
func truncateReplyContextBody(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	out := truncateRunes(s, maxRunes)
	return strings.TrimSpace(out) + "\n...(truncated)"
}

// formatInboundWithReplyContext builds the final user message for RunAgent. Strategy A: if userText is a comma command, returns userText only (no quote block).
func formatInboundWithReplyContext(ids replyContextIDs, parentBody string, userText string, maxParentRunes int) string {
	if isCommaCommandMessage(userText) {
		return userText
	}
	parentBody = strings.TrimSpace(parentBody)
	if parentBody == "" {
		if strings.TrimSpace(userText) == "" {
			return "[empty message]"
		}
		return userText
	}
	parentBody = truncateReplyContextBody(parentBody, maxParentRunes)

	var b strings.Builder
	b.WriteString(quotedBlockStart)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "parent_message_id: %s\n", ids.ParentMessageID)
	if ids.CurrentMessageID != "" {
		fmt.Fprintf(&b, "current_message_id: %s\n", ids.CurrentMessageID)
	}
	if ids.RootMessageID != "" {
		fmt.Fprintf(&b, "root_message_id: %s\n", ids.RootMessageID)
	}
	b.WriteString("---\n")
	b.WriteString(parentBody)
	b.WriteString("\n")
	b.WriteString(quotedBlockEnd)
	b.WriteByte('\n')
	b.WriteByte('\n')
	b.WriteString(userText)
	return b.String()
}

func inboundUserContentOrEmptyFallback(userText string) string {
	if strings.TrimSpace(userText) == "" {
		return "[empty message]"
	}
	return userText
}

// mergeInboundReplyContext loads parent message when enabled and parent_id is set, then formats model content.
func (p *FeishuPlugin) mergeInboundReplyContext(ctx context.Context, bot *BotInstance, ev *larkim.EventMessage, userText string) string {
	if isCommaCommandMessage(userText) {
		if pid := stringValue(ev.ParentId); pid != "" {
			log.Printf("feishu: reply context skipped (comma command overrides quote) parentId=%q", pid)
		}
		return inboundUserContentOrEmptyFallback(userText)
	}
	if !p.cfg.InboundReplyContextEnabled {
		return inboundUserContentOrEmptyFallback(userText)
	}
	parentID := stringValue(ev.ParentId)
	if parentID == "" {
		return inboundUserContentOrEmptyFallback(userText)
	}

	apiMsg, err := p.fetchParentMessage(ctx, bot, parentID)
	if err != nil {
		if errors.Is(err, errQuotedMessageDeleted) {
			log.Printf("feishu: reply context parent deleted parentID=%q", parentID)
		} else {
			log.Printf("feishu: reply context fetch parent failed parentID=%q: %v", parentID, err)
		}
		return inboundUserContentOrEmptyFallback(userText)
	}

	parentEv := larkMessageToEventMessage(apiMsg)
	if parentEv == nil || parentEv.Content == nil || strings.TrimSpace(*parentEv.Content) == "" {
		log.Printf("feishu: reply context parent has no content parentID=%q", parentID)
		return inboundUserContentOrEmptyFallback(userText)
	}

	parentBody := p.buildInboundUserContent(ctx, bot, parentEv)
	parentBody = strings.TrimSpace(parentBody)
	if parentBody == "" {
		return inboundUserContentOrEmptyFallback(userText)
	}

	ids := replyContextIDs{
		ParentMessageID:  parentID,
		CurrentMessageID: stringValue(ev.MessageId),
		RootMessageID:    stringValue(ev.RootId),
	}
	return formatInboundWithReplyContext(ids, parentBody, userText, p.cfg.inboundReplyContextMaxRunes())
}
