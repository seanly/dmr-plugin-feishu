package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// uploadFileToFeishu uploads bytes as a generic stream file and returns file_key.
func (p *FeishuPlugin) uploadFileToFeishu(ctx context.Context, fileName string, r io.Reader) (string, error) {
	if p.lc == nil {
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
	resp, err := p.lc.Im.V1.File.Create(ctx, req)
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

// sendFileForJob sends msg_type=file to the same destination as replyAgentOutput (thread reply vs chat create).
func (p *FeishuPlugin) sendFileForJob(ctx context.Context, job *inboundJob, fileKey string) error {
	if p.lc == nil {
		return fmt.Errorf("feishu client not initialized")
	}
	payload, err := json.Marshal(map[string]string{"file_key": fileKey})
	if err != nil {
		return err
	}
	content := string(payload)
	uuid := fmt.Sprintf("dmr-feishu-file-%d", time.Now().UnixNano())

	if job.InThread && job.TriggerMessageID != "" {
		body := larkim.NewReplyMessageReqBodyBuilder().
			MsgType(larkim.MsgTypeFile).
			Content(content).
			ReplyInThread(true).
			Uuid(uuid).
			Build()
		req := larkim.NewReplyMessageReqBuilder().
			MessageId(job.TriggerMessageID).
			Body(body).
			Build()
		resp, err := p.lc.Im.V1.Message.Reply(ctx, req)
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
			ReceiveId(job.ChatID).
			MsgType(larkim.MsgTypeFile).
			Content(content).
			Uuid(uuid).
			Build()).
		Build()
	resp, err := p.lc.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return err
	}
	if !resp.Success() {
		return fmt.Errorf("feishu file create message: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// sendFileFromReader uploads then sends file to the active Feishu session.
func (p *FeishuPlugin) sendFileFromReader(ctx context.Context, job *inboundJob, fileName string, r io.Reader) (fileKey string, err error) {
	key, err := p.uploadFileToFeishu(ctx, fileName, r)
	if err != nil {
		return "", err
	}
	if err := p.sendFileForJob(ctx, job, key); err != nil {
		log.Printf("feishu: file message send failed after upload file_key=%q: %v", key, err)
		return "", err
	}
	return key, nil
}
