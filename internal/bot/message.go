package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/seanly/dmr-plugin-feishu/pkg/utils"
)

const (
	maxFeishuTextRunes             = 18000
	maxFeishuApprovalMarkdownRunes = 14000
	maxSendRetries                 = 3
)

// isRetryableError checks if an error is retryable (network/connection errors).
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection") ||
		strings.Contains(msg, "shut down") ||
		strings.Contains(msg, "reset") ||
		strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "EOF")
}

// MessagePostMD is a custom IM post element for markdown.
type MessagePostMD struct {
	Text string `json:"text,omitempty"`
}

// Tag returns the element tag.
func (m *MessagePostMD) Tag() string { return "md" }

// IsPost marks this as a post element.
func (m *MessagePostMD) IsPost() {}

// MarshalJSON implements json.Marshaler.
func (m *MessagePostMD) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"tag":  "md",
		"text": m.Text,
	})
}

// BuildFeishuPostMarkdownContent builds post content with markdown.
func BuildFeishuPostMarkdownContent(markdown string) (string, error) {
	inner := larkim.NewMessagePost().ZhCn(
		larkim.NewMessagePostContent().
			ContentTitle("").
			AppendContent([]larkim.MessagePostElement{
				&MessagePostMD{Text: markdown},
			}),
	)
	return inner.Build()
}

// SendTextToChat sends plain text message to chat with retry.
func (c *Client) SendTextToChat(ctx context.Context, chatID, text string) error {
	if c.Lark == nil {
		return fmt.Errorf("feishu client not initialized")
	}
	payload, err := json.Marshal(map[string]string{"text": utils.TruncateRunes(text, maxFeishuTextRunes)})
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < maxSendRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
			log.Printf("feishu: SendTextToChat retry %d/%d after error: %v", attempt, maxSendRetries-1, lastErr)
		}

		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypeText).
				Content(string(payload)).
				Uuid(fmt.Sprintf("dmr-feishu-%d-%d", time.Now().UnixNano(), attempt)).
				Build()).
			Build()
		resp, err := c.Lark.Im.V1.Message.Create(ctx, req)
		if err != nil {
			lastErr = err
			if isRetryableError(err) && attempt < maxSendRetries-1 {
				continue
			}
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("feishu create message: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}
	return lastErr
}
// SendMarkdownPostToChat sends markdown post to chat with retry.
func (c *Client) SendMarkdownPostToChat(ctx context.Context, chatID, markdown string) error {
	if c.Lark == nil {
		return fmt.Errorf("feishu client not initialized")
	}
	postContent, err := BuildFeishuPostMarkdownContent(markdown)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < maxSendRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
			log.Printf("feishu: SendMarkdownPostToChat retry %d/%d after error: %v", attempt, maxSendRetries-1, lastErr)
		}

		uuid := fmt.Sprintf("dmr-feishu-md-%d-%d", time.Now().UnixNano(), attempt)
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypePost).
				Content(postContent).
				Uuid(uuid).
				Build()).
			Build()
		resp, err := c.Lark.Im.V1.Message.Create(ctx, req)
		if err != nil {
			lastErr = err
			if isRetryableError(err) && attempt < maxSendRetries-1 {
				continue
			}
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("feishu post create: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}
	return lastErr
}

// SendApprovalMessageToChat sends approval message (tries markdown first, then plain).
func (c *Client) SendApprovalMessageToChat(ctx context.Context, chatID, markdown string) error {
	md := utils.TruncateRunes(markdown, maxFeishuApprovalMarkdownRunes)
	if err := c.SendMarkdownPostToChat(ctx, chatID, md); err != nil {
		log.Printf("feishu: approval post/md failed, fallback text: %v", err)
		return c.SendTextToChat(ctx, chatID, md)
	}
	return nil
}

// DeliverIMTextToP2PChat sends a new message to a p2p chat by chat_id.
func (c *Client) DeliverIMTextToP2PChat(ctx context.Context, chatID, text string, preferMarkdown bool) error {
	if chatID == "" {
		return fmt.Errorf("feishu: empty chat_id")
	}
	if preferMarkdown {
		md := utils.TruncateRunes(text, maxFeishuTextRunes)
		if err := c.SendMarkdownPostToChat(ctx, chatID, md); err != nil {
			log.Printf("feishu: send_text post/md failed, fallback text: %v", err)
			return c.SendTextToChat(ctx, chatID, md)
		}
		return nil
	}
	return c.SendTextToChat(ctx, chatID, text)
}

// DeliverIMTextForJob sends text to the same destination (thread vs main chat).
// preferMarkdown: when true, try Feishu post+md first then plain text; when false, send plain text only.
func (c *Client) DeliverIMTextForJob(ctx context.Context, chatID, triggerMessageID string, inThread bool, text string, preferMarkdown bool) error {
	if c.Lark == nil {
		return fmt.Errorf("feishu client not initialized")
	}

	textPayload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	uuid := fmt.Sprintf("dmr-feishu-%d", time.Now().UnixNano())

	if inThread && triggerMessageID != "" {
		if !preferMarkdown {
			body := larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeText).
				Content(string(textPayload)).
				ReplyInThread(true).
				Uuid(uuid).
				Build()
			req := larkim.NewReplyMessageReqBuilder().
				MessageId(triggerMessageID).
				Body(body).
				Build()
			resp, err := c.Lark.Im.V1.Message.Reply(ctx, req)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("feishu reply in thread: code=%d msg=%s", resp.Code, resp.Msg)
			}
			return nil
		}
		// Try rich post with markdown first
		postContent, postErr := BuildFeishuPostMarkdownContent(text)
		if postErr == nil {
			body := larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypePost).
				Content(postContent).
				ReplyInThread(true).
				Uuid(uuid).
				Build()
			req := larkim.NewReplyMessageReqBuilder().
				MessageId(triggerMessageID).
				Body(body).
				Build()
			resp, err := c.Lark.Im.V1.Message.Reply(ctx, req)
			if err == nil && resp != nil && resp.Success() {
				return nil
			}
			if err != nil {
				log.Printf("feishu: post reply failed (thread) err=%v", err)
			} else if resp != nil {
				log.Printf("feishu: post reply failed (thread) code=%d msg=%s", resp.Code, resp.Msg)
			}
		}

		body := larkim.NewReplyMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeText).
			Content(string(textPayload)).
			ReplyInThread(true).
			Uuid(uuid).
			Build()
		req := larkim.NewReplyMessageReqBuilder().
			MessageId(triggerMessageID).
			Body(body).
			Build()
		resp, err := c.Lark.Im.V1.Message.Reply(ctx, req)
		if err != nil {
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("feishu reply in thread: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}

	// Not in thread - create new message
	if !preferMarkdown {
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypeText).
				Content(string(textPayload)).
				Uuid(uuid).
				Build()).
			Build()
		resp, err := c.Lark.Im.V1.Message.Create(ctx, req)
		if err != nil {
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("feishu create message: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}

	// Try rich post with markdown first
	postContent, postErr := BuildFeishuPostMarkdownContent(text)
	if postErr == nil {
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypePost).
				Content(postContent).
				Uuid(uuid).
				Build()).
			Build()
		resp, err := c.Lark.Im.V1.Message.Create(ctx, req)
		if err == nil && resp != nil && resp.Success() {
			return nil
		}
		if err != nil {
			log.Printf("feishu: post create failed err=%v", err)
		} else if resp != nil {
			log.Printf("feishu: post create failed code=%d msg=%s", resp.Code, resp.Msg)
		}
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeText).
			Content(string(textPayload)).
			Uuid(uuid).
			Build()).
		Build()
	resp, err := c.Lark.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu create message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}
