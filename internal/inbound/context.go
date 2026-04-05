package inbound

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	quotedBlockStart = "<<< feishu_quoted_message"
	quotedBlockEnd   = ">>> end_feishu_quoted_message"
)

// ReplyContextIDs holds Feishu message ids for the quoted block header.
type ReplyContextIDs struct {
	ParentMessageID  string
	CurrentMessageID string
	RootMessageID    string
}

// ErrQuotedMessageDeleted is returned when the quoted parent message was deleted.
var ErrQuotedMessageDeleted = errors.New("feishu: quoted parent message was deleted")

// LarkMessageToEventMessage maps API Message (message/get) into EventMessage shape.
func LarkMessageToEventMessage(m *larkim.Message) *larkim.EventMessage {
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

// MessageFromGetRespData extracts message from get response.
func MessageFromGetRespData(data *larkim.GetMessageRespData) *larkim.Message {
	if data == nil {
		return nil
	}
	if len(data.Items) > 0 && data.Items[0] != nil {
		return data.Items[0]
	}
	return nil
}

// FetchParentMessage retrieves a parent message by ID.
func FetchParentMessage(ctx context.Context, client *lark.Client, parentMessageID string, timeout time.Duration) (*larkim.Message, error) {
	if client == nil {
		return nil, fmt.Errorf("feishu client not initialized")
	}
	parentMessageID = strings.TrimSpace(parentMessageID)
	if parentMessageID == "" {
		return nil, fmt.Errorf("empty parent_message_id")
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := larkim.NewGetMessageReqBuilder().
		MessageId(parentMessageID).
		UserIdType(larkim.UserIdTypeGetMessageOpenId).
		Build()
	resp, err := client.Im.V1.Message.Get(dctx, req)
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
	m := MessageFromGetRespData(resp.Data)
	if m == nil {
		return nil, fmt.Errorf("feishu message.get: empty data.items")
	}
	if m.Deleted != nil && *m.Deleted {
		return nil, ErrQuotedMessageDeleted
	}
	return m, nil
}

// IsCommaCommandMessage checks if text is a comma command.
func IsCommaCommandMessage(userText string) bool {
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

// TruncateReplyContextBody caps parent body by runes and appends a truncation line if needed.
func TruncateReplyContextBody(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return s
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	if len([]rune(s)) > maxRunes {
		s = string([]rune(s)[:maxRunes]) + "\n...(truncated)"
	}
	return strings.TrimSpace(s)
}

// FormatInboundWithReplyContext builds the final user message for RunAgent.
func FormatInboundWithReplyContext(ids ReplyContextIDs, parentBody string, userText string, maxParentRunes int) string {
	if IsCommaCommandMessage(userText) {
		return userText
	}
	parentBody = strings.TrimSpace(parentBody)
	if parentBody == "" {
		if strings.TrimSpace(userText) == "" {
			return "[empty message]"
		}
		return userText
	}
	parentBody = TruncateReplyContextBody(parentBody, maxParentRunes)

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

// InboundUserContentOrEmptyFallback returns fallback for empty content.
func InboundUserContentOrEmptyFallback(userText string) string {
	if strings.TrimSpace(userText) == "" {
		return "[empty message]"
	}
	return userText
}
