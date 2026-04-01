package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const (
	maxFeishuTextRunes            = 18000
	maxFeishuApprovalMarkdownRunes = 14000 // post body ~30KB limit; stay conservative
)

// messagePostMD is a custom IM post element that lets Feishu parse markdown inside rich text.
// Feishu "post" supports a node with: { "tag": "md", "text": "<markdown>" }.
type messagePostMD struct {
	Text string `json:"text,omitempty"`
}

func (m *messagePostMD) Tag() string { return "md" }
func (m *messagePostMD) IsPost()    {}
func (m *messagePostMD) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"tag":  "md",
		"text": m.Text,
	})
}

func buildFeishuPostMarkdownContent(markdown string) (string, error) {
	// Feishu expects: content = JSON-serialized string, whose JSON includes { "post": { ... } }.
	inner := larkim.NewMessagePost().ZhCn(
		larkim.NewMessagePostContent().
			ContentTitle("").
			AppendContent([]larkim.MessagePostElement{
				&messagePostMD{Text: markdown},
			}),
	)

	innerStr, err := inner.Build()
	if err != nil {
		return "", err
	}
	return innerStr, nil
}

func (b *BotInstance) sendTextToChat(ctx context.Context, chatID, text string) error {
	if b.lc == nil {
		return fmt.Errorf("feishu client not initialized")
	}
	payload, err := json.Marshal(map[string]string{"text": truncateRunes(text, maxFeishuTextRunes)})
	if err != nil {
		return err
	}
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeText).
			Content(string(payload)).
			Uuid(fmt.Sprintf("dmr-feishu-%d", time.Now().UnixNano())).
			Build()).
		Build()
	resp, err := b.lc.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu create message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// sendMarkdownPostToChat sends msg_type=post with a single zh_cn "md" node (standard Markdown).
// Caller should cap markdown length if needed; this does not truncate.
func (b *BotInstance) sendMarkdownPostToChat(ctx context.Context, chatID, markdown string) error {
	if b.lc == nil {
		return fmt.Errorf("feishu client not initialized")
	}
	postContent, err := buildFeishuPostMarkdownContent(markdown)
	if err != nil {
		return err
	}
	uuid := fmt.Sprintf("dmr-feishu-md-%d", time.Now().UnixNano())
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypePost).
			Content(postContent).
			Uuid(uuid).
			Build()).
		Build()
	resp, err := b.lc.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu post create: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// sendApprovalMessageToChat tries post+md first, then falls back to plain text.
func (b *BotInstance) sendApprovalMessageToChat(ctx context.Context, chatID, markdown string) error {
	md := truncateRunes(markdown, maxFeishuApprovalMarkdownRunes)
	if err := b.sendMarkdownPostToChat(ctx, chatID, md); err != nil {
		log.Printf("feishu: approval post/md failed, fallback text: %v", err)
		return b.sendTextToChat(ctx, chatID, md)
	}
	return nil
}

func (p *FeishuPlugin) replyAgentOutput(ctx context.Context, job *inboundJob, output string) error {
	text := truncateRunes(output, maxFeishuTextRunes)
	return job.Bot.deliverIMTextForJob(ctx, job, text, true)
}

// deliverIMTextForJob sends text to the same destination as replyAgentOutput (thread vs main chat).
// preferMarkdown: when true, try Feishu post+md first then plain text; when false, send plain text only.
func (b *BotInstance) deliverIMTextForJob(ctx context.Context, job *inboundJob, text string, preferMarkdown bool) error {
	if b.lc == nil {
		return fmt.Errorf("feishu client not initialized")
	}
	if job == nil {
		return fmt.Errorf("feishu: nil inbound job")
	}

	textPayload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	uuid := fmt.Sprintf("dmr-feishu-%d", time.Now().UnixNano())

	if job.InThread && job.TriggerMessageID != "" {
		if !preferMarkdown {
			body := larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypeText).
				Content(string(textPayload)).
				ReplyInThread(true).
				Uuid(uuid).
				Build()
			req := larkim.NewReplyMessageReqBuilder().
				MessageId(job.TriggerMessageID).
				Body(body).
				Build()
			resp, err := b.lc.Im.V1.Message.Reply(ctx, req)
			if err != nil {
				return err
			}
			if !resp.Success() {
				return fmt.Errorf("feishu reply in thread: code=%d msg=%s", resp.Code, resp.Msg)
			}
			return nil
		}
		// Try rich post with markdown first; fallback to plain text.
		postContent, postErr := buildFeishuPostMarkdownContent(text)
		if postErr == nil {
			body := larkim.NewReplyMessageReqBodyBuilder().
				MsgType(larkim.MsgTypePost).
				Content(postContent).
				ReplyInThread(true).
				Uuid(uuid).
				Build()
			req := larkim.NewReplyMessageReqBuilder().
				MessageId(job.TriggerMessageID).
				Body(body).
				Build()
			resp, err := b.lc.Im.V1.Message.Reply(ctx, req)
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
			MessageId(job.TriggerMessageID).
			Body(body).
			Build()
		resp, err := b.lc.Im.V1.Message.Reply(ctx, req)
		if err != nil {
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("feishu reply in thread: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}

	if !preferMarkdown {
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(job.ChatID).
				MsgType(larkim.MsgTypeText).
				Content(string(textPayload)).
				Uuid(uuid).
				Build()).
			Build()
		resp, err := b.lc.Im.V1.Message.Create(ctx, req)
		if err != nil {
			return err
		}
		if !resp.Success() {
			return fmt.Errorf("feishu create message: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}

	// Try rich post with markdown first; fallback to plain text.
	postContent, postErr := buildFeishuPostMarkdownContent(text)
	if postErr == nil {
		req := larkim.NewCreateMessageReqBuilder().
			ReceiveIdType(larkim.ReceiveIdTypeChatId).
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(job.ChatID).
				MsgType(larkim.MsgTypePost).
				Content(postContent).
				Uuid(uuid).
				Build()).
			Build()
		resp, err := b.lc.Im.V1.Message.Create(ctx, req)
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
			ReceiveId(job.ChatID).
			MsgType(larkim.MsgTypeText).
			Content(string(textPayload)).
			Uuid(uuid).
			Build()).
		Build()
	resp, err := b.lc.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu create message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// deliverIMTextToP2PChat sends a new message to a p2p chat by chat_id (no thread context).
// Used when RunAgent was not triggered from Feishu (e.g. cron) so there is no active inboundJob.
func (b *BotInstance) deliverIMTextToP2PChat(ctx context.Context, chatID, text string, preferMarkdown bool) error {
	if strings.TrimSpace(chatID) == "" {
		return fmt.Errorf("feishu: empty chat_id")
	}
	if preferMarkdown {
		md := truncateRunes(text, maxFeishuTextRunes)
		if err := b.sendMarkdownPostToChat(ctx, chatID, md); err != nil {
			log.Printf("feishu: send_text post/md failed, fallback text: %v", err)
			return b.sendTextToChat(ctx, chatID, md)
		}
		return nil
	}
	return b.sendTextToChat(ctx, chatID, text)
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if len(runes) > maxRunes {
		s = string(runes[:maxRunes]) + "\n\n…(truncated)"
	}
	return s
}
