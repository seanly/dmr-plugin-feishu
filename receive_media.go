package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

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

func (p *FeishuPlugin) downloadMessageResource(ctx context.Context, msgID string, parsed ParsedInbound) (absPath string, savedName string, err error) {
	if p.lc == nil {
		return "", "", fmt.Errorf("feishu client not initialized")
	}
	resType := messageResourceType(parsed)
	key := parsed.resourceKey()
	if resType == "" || key == "" || msgID == "" {
		return "", "", fmt.Errorf("missing message resource type, key, or message_id")
	}

	dctx, cancel := context.WithTimeout(ctx, p.cfg.inboundMediaTimeout())
	defer cancel()

	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(msgID).
		FileKey(key).
		Type(resType).
		Build()
	resp, err := p.lc.Im.V1.MessageResource.Get(dctx, req)
	if err != nil {
		return "", "", err
	}
	if resp.File == nil {
		return "", "", fmt.Errorf("feishu message resource failed code=%d msg=%s", resp.Code, resp.Msg)
	}

	max := p.cfg.inboundMediaMaxBytes()
	limited := io.LimitReader(resp.File, max+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", "", err
	}
	if int64(len(data)) > max {
		return "", "", fmt.Errorf("resource exceeds inbound_media_max_bytes (%d)", max)
	}

	name := sanitizeFileName(resp.FileName)
	if name == "" || name == "file.bin" {
		name = sanitizeFileName(parsed.FileName)
	}
	if name == "" || name == "file.bin" {
		if resType == "image" {
			name = "image.jpg"
		} else {
			name = "file.bin"
		}
	}

	root, err := p.cfg.inboundStorageRoot()
	if err != nil {
		return "", "", err
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

func (p *FeishuPlugin) inboundNonTextToPrompt(ctx context.Context, parsed ParsedInbound, msgID string) string {
	if parsed.MsgType == larkim.MsgTypePost {
		var b strings.Builder
		b.WriteString("[Feishu inbound — msg_type=post]\n")
		if parsed.PostPlain != "" {
			b.WriteString(parsed.PostPlain)
			b.WriteByte('\n')
		}
		b.WriteString("\nnote: Rich-text embeddings are not auto-downloaded; send image or file as a separate message for inbound_media.\n")
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

	if !p.cfg.InboundMediaEnabled || !parsed.NeedsDownload || strings.TrimSpace(msgID) == "" {
		b.WriteString("status: summary_only\n")
		if parsed.NeedsDownload {
			b.WriteString("hint: set inbound_media_enabled:true to download into workspace; requires Feishu scopes for message resources.\n")
		}
		return strings.TrimSpace(b.String())
	}

	path, _, err := p.downloadMessageResource(ctx, msgID, parsed)
	if err != nil {
		fmt.Fprintf(&b, "status: download_failed\nreason: %v\n", err)
		return strings.TrimSpace(b.String())
	}
	fmt.Fprintf(&b, "local_path: %s\n", path)
	b.WriteString("status: downloaded\n")
	return strings.TrimSpace(b.String())
}

func (p *FeishuPlugin) buildInboundUserContent(ctx context.Context, message *larkim.EventMessage) string {
	parsed := parseFeishuInboundMessage(message)
	msgID := stringValue(message.MessageId)

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
		return p.inboundNonTextToPrompt(ctx, parsed, msgID)
	}
	if strings.TrimSpace(parsed.RawContent) != "" {
		return fmt.Sprintf("[Feishu inbound — msg_type unknown]\n%s", parsed.RawContent)
	}
	return ""
}

func cleanupInboundOldDays(cfg FeishuConfig) error {
	if cfg.InboundMediaRetentionDays <= 0 {
		return nil
	}
	root, err := cfg.inboundStorageRoot()
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
	cutoff := time.Now().AddDate(0, 0, -cfg.InboundMediaRetentionDays)
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

func (p *FeishuPlugin) scheduleInboundRetentionCleanup() {
	if p.cfg.InboundMediaRetentionDays <= 0 {
		return
	}
	p.runMu.Lock()
	parent := p.runCtx
	p.runMu.Unlock()
	if parent == nil {
		return
	}
	go func() {
		select {
		case <-parent.Done():
			return
		case <-time.After(2 * time.Second):
		}
		if err := cleanupInboundOldDays(p.cfg); err != nil {
			log.Printf("feishu: inbound retention cleanup: %v", err)
		}
	}()
}
