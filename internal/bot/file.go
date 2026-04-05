package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/seanly/dmr-plugin-feishu/internal/inbound"
	"github.com/seanly/dmr-plugin-feishu/pkg/utils"
)

// ConfigAccessor provides access to configuration needed by file operations.
type ConfigAccessor interface {
	InboundMediaEnabled() bool
	InboundMediaMaxBytes() int64
	InboundMediaTimeout() time.Duration
	InboundStorageRoot() (string, error)
	InboundMediaRetentionDays() int
}

// sanitizeMsgIDForFile sanitizes message ID for use in filename.
func sanitizeMsgIDForFile(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "nomsg"
	}
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, "\\", "_")
	if len(id) > 120 {
		id = id[:120]
	}
	return id
}

// DownloadMessageResource downloads a message resource (image/file) from Feishu.
func DownloadMessageResource(ctx context.Context, client *lark.Client, msgID string, parsed inbound.ParsedInbound, maxBytes int64, timeout time.Duration, root string) (absPath string, savedName string, err error) {
	if client == nil {
		return "", "", fmt.Errorf("feishu client not initialized")
	}
	resType := inbound.MessageResourceType(parsed)
	key := parsed.ResourceKey()
	if resType == "" || key == "" || msgID == "" {
		return "", "", fmt.Errorf("missing message resource type, key, or message_id")
	}

	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(msgID).
		FileKey(key).
		Type(resType).
		Build()
	resp, err := client.Im.V1.MessageResource.Get(dctx, req)
	if err != nil {
		return "", "", err
	}
	if resp.File == nil {
		return "", "", fmt.Errorf("feishu message resource failed code=%d msg=%s", resp.Code, resp.Msg)
	}

	limited := io.LimitReader(resp.File, maxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", "", err
	}
	if int64(len(data)) > maxBytes {
		return "", "", fmt.Errorf("resource exceeds max_bytes (%d)", maxBytes)
	}

	name := utils.SanitizeFileName(resp.FileName, 200)
	if name == "" || name == "file.bin" {
		name = utils.SanitizeFileName(parsed.FileName, 200)
	}
	if name == "" || name == "file.bin" {
		if resType == "image" {
			name = "image.jpg"
		} else {
			name = "file.bin"
		}
	}

	dayDir := time.Now().Format("2006-01-02")
	dir := filepath.Join(root, dayDir)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", "", err
	}
	fname := fmt.Sprintf("%s_%s", sanitizeMsgIDForFile(msgID), name)
	abs := filepath.Join(dir, fname)
	relCheck, err := filepath.Rel(root, abs)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("resolved path escapes inbound root")
	}
	if err := os.WriteFile(abs, data, 0600); err != nil {
		return "", "", err
	}
	return abs, name, nil
}

// UploadFileToFeishu uploads bytes as a generic stream file and returns file_key.
func (c *Client) UploadFileToFeishu(ctx context.Context, fileName string, r io.Reader) (string, error) {
	if c.Lark == nil {
		return "", fmt.Errorf("feishu client not initialized")
	}
	body := larkim.NewCreateFileReqBodyBuilder().
		FileType(larkim.FileTypeStream).
		FileName(fileName).
		File(r).
		Build()
	req := larkim.NewCreateFileReqBuilder().
		Body(body).
		Build()
	resp, err := c.Lark.Im.V1.File.Create(ctx, req)
	if err != nil {
		return "", err
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu file upload: code=%d msg=%s", resp.Code, resp.Msg)
	}
	if resp.Data == nil || resp.Data.FileKey == nil || *resp.Data.FileKey == "" {
		return "", fmt.Errorf("feishu file upload: empty file_key")
	}
	return *resp.Data.FileKey, nil
}

// SendFileForJob sends msg_type=file to the job's destination (thread reply vs chat create).
func (c *Client) SendFileForJob(ctx context.Context, chatID, triggerMessageID string, inThread bool, fileKey string) error {
	if c.Lark == nil {
		return fmt.Errorf("feishu client not initialized")
	}
	payload, err := json.Marshal(map[string]string{"file_key": fileKey})
	if err != nil {
		return err
	}
	content := string(payload)
	uuid := fmt.Sprintf("dmr-feishu-file-%d", time.Now().UnixNano())

	if inThread && triggerMessageID != "" {
		body := larkim.NewReplyMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeFile).
			Content(content).
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
			return fmt.Errorf("feishu file reply in thread: code=%d msg=%s", resp.Code, resp.Msg)
		}
		return nil
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType(larkim.MsgTypeFile).
			Content(content).
			Uuid(uuid).
			Build()).
		Build()
	resp, err := c.Lark.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu file create message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// SendFileFromReader uploads then sends file.
func (c *Client) SendFileFromReader(ctx context.Context, chatID, triggerMessageID string, inThread bool, fileName string, r io.Reader) (fileKey string, err error) {
	key, err := c.UploadFileToFeishu(ctx, fileName, r)
	if err != nil {
		return "", err
	}
	if err := c.SendFileForJob(ctx, chatID, triggerMessageID, inThread, key); err != nil {
		log.Printf("feishu: file message send failed after upload file_key=%q: %v", key, err)
		return "", err
	}
	return key, nil
}

// CleanupInboundOldDays removes old inbound media directories.
func CleanupInboundOldDays(cfg ConfigAccessor) error {
	if cfg.InboundMediaRetentionDays() <= 0 {
		return nil
	}
	root, err := cfg.InboundStorageRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -cfg.InboundMediaRetentionDays())
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		t, err := time.ParseInLocation("2006-01-02", name, time.Local)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			full := filepath.Join(root, name)
			if err := os.RemoveAll(full); err != nil {
				log.Printf("feishu: inbound retention: remove %q: %v", full, err)
			} else {
				log.Printf("feishu: inbound retention: removed old dir %q", full)
			}
		}
	}
	return nil
}

// BuildInboundUserContent builds user content from a message.
func BuildInboundUserContent(ctx context.Context, client *lark.Client, cfg ConfigAccessor, message *larkim.EventMessage, msgID string) string {
	parsed := inbound.ParseFeishuInboundMessage(message)

	if parsed.MsgType == larkim.MsgTypeText {
		t := strings.TrimSpace(parsed.TextBody)
		if t != "" {
			return t
		}
		if strings.TrimSpace(parsed.RawContent) != "" {
			return parsed.RawContent
		}
		return ""
	}
	if parsed.MsgType != "" {
		return inboundNonTextToPrompt(ctx, client, cfg, parsed, msgID)
	}
	if strings.TrimSpace(parsed.RawContent) != "" {
		return fmt.Sprintf("[Feishu inbound — msg_type unknown]\n%s", parsed.RawContent)
	}
	return ""
}

func inboundNonTextToPrompt(ctx context.Context, client *lark.Client, cfg ConfigAccessor, parsed inbound.ParsedInbound, msgID string) string {
	if parsed.MsgType == larkim.MsgTypePost {
		var b strings.Builder
		b.WriteString("[Feishu inbound — msg_type=post]\n")
		if parsed.PostPlain != "" {
			b.WriteString(parsed.PostPlain)
			b.WriteByte('\n')
		}
		b.WriteString("\nnote: Rich-text embeddings are not auto-downloaded.\n")
		return strings.TrimSpace(b.String())
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[Feishu inbound attachment]\nmsg_type: %s\n", parsed.MsgType)
	if parsed.FileName != "" {
		fmt.Fprintf(&b, "file_name: %s\n", parsed.FileName)
	}
	if parsed.ImageKey != "" {
		fmt.Fprintf(&b, "feishu_image_key: %s\n", parsed.ImageKey)
	}
	if parsed.FileKey != "" {
		fmt.Fprintf(&b, "feishu_file_key: %s\n", parsed.FileKey)
	}

	if !cfg.InboundMediaEnabled() || !parsed.NeedsDownload || strings.TrimSpace(msgID) == "" {
		b.WriteString("status: summary_only\n")
		if parsed.NeedsDownload {
			b.WriteString("hint: set inbound_media_enabled:true to download into workspace.\n")
		}
		return strings.TrimSpace(b.String())
	}

	root, _ := cfg.InboundStorageRoot()
	path, _, err := DownloadMessageResource(ctx, client, msgID, parsed, cfg.InboundMediaMaxBytes(), cfg.InboundMediaTimeout(), root)
	if err != nil {
		fmt.Fprintf(&b, "status: download_failed\nreason: %v\n", err)
		return strings.TrimSpace(b.String())
	}
	fmt.Fprintf(&b, "local_path: %s\n", path)
	b.WriteString("status: downloaded\n")
	return strings.TrimSpace(b.String())
}

// MergeInboundReplyContext merges reply context.
func MergeInboundReplyContext(ctx context.Context, client *lark.Client, cfg ConfigAccessor, message *larkim.EventMessage, userText string) string {
	if inbound.IsCommaCommandMessage(userText) {
		return inbound.InboundUserContentOrEmptyFallback(userText)
	}
	if !cfg.InboundMediaEnabled() {
		return inbound.InboundUserContentOrEmptyFallback(userText)
	}
	parentID := inbound.StringValue(message.ParentId)
	if parentID == "" {
		return inbound.InboundUserContentOrEmptyFallback(userText)
	}

	apiMsg, err := inbound.FetchParentMessage(ctx, client, parentID, cfg.InboundMediaTimeout())
	if err != nil {
		return inbound.InboundUserContentOrEmptyFallback(userText)
	}

	parentEv := inbound.LarkMessageToEventMessage(apiMsg)
	if parentEv == nil || parentEv.Content == nil || strings.TrimSpace(*parentEv.Content) == "" {
		return inbound.InboundUserContentOrEmptyFallback(userText)
	}

	parentBody := BuildInboundUserContent(ctx, client, cfg, parentEv, inbound.StringValue(parentEv.MessageId))
	parentBody = strings.TrimSpace(parentBody)
	if parentBody == "" {
		return inbound.InboundUserContentOrEmptyFallback(userText)
	}

	ids := inbound.ReplyContextIDs{
		ParentMessageID:  parentID,
		CurrentMessageID: inbound.StringValue(message.MessageId),
		RootMessageID:    inbound.StringValue(message.RootId),
	}
	return inbound.FormatInboundWithReplyContext(ids, parentBody, userText, 8000)
}
