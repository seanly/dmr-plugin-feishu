package main

import (
	"path/filepath"
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func ptr(s string) *string { return &s }

func TestParseFeishuInboundMessage_text(t *testing.T) {
	raw := `{"text":"hello"}`
	msg := &larkim.EventMessage{
		MessageType: ptr(larkim.MsgTypeText),
		Content:     &raw,
	}
	p := parseFeishuInboundMessage(msg)
	if p.MsgType != larkim.MsgTypeText || p.TextBody != "hello" {
		t.Fatalf("got %+v", p)
	}
}

func TestParseFeishuInboundMessage_image(t *testing.T) {
	raw := `{"image_key":"img_abc"}`
	msg := &larkim.EventMessage{
		MessageType: ptr(larkim.MsgTypeImage),
		Content:     &raw,
	}
	p := parseFeishuInboundMessage(msg)
	if !p.NeedsDownload || p.ImageKey != "img_abc" {
		t.Fatalf("got %+v", p)
	}
}

func TestParseFeishuInboundMessage_file(t *testing.T) {
	raw := `{"file_key":"file_xyz","file_name":"a b.pdf"}`
	msg := &larkim.EventMessage{
		MessageType: ptr(larkim.MsgTypeFile),
		Content:     &raw,
	}
	p := parseFeishuInboundMessage(msg)
	if !p.NeedsDownload || p.FileKey != "file_xyz" || p.FileName != "a b.pdf" {
		t.Fatalf("got %+v", p)
	}
}

func TestParseFeishuInboundMessage_postPlain(t *testing.T) {
	raw := `{"zh_cn":{"title":"t","content":[[[{"tag":"text","text":"Hello"},{"tag":"text","text":"World"}]]]}}`
	msg := &larkim.EventMessage{
		MessageType: ptr(larkim.MsgTypePost),
		Content:     &raw,
	}
	p := parseFeishuInboundMessage(msg)
	if !strings.Contains(p.PostPlain, "Hello") || !strings.Contains(p.PostPlain, "World") {
		t.Fatalf("PostPlain=%q", p.PostPlain)
	}
}

func TestSanitizeMsgIDForFile(t *testing.T) {
	if got := sanitizeMsgIDForFile("om_a/b"); got != "om_a_b" {
		t.Fatal(got)
	}
	if got := sanitizeMsgIDForFile(""); got != "nomsg" {
		t.Fatal(got)
	}
}

func TestParseFeishuConfig_replyContextDefaults(t *testing.T) {
	c, err := parseFeishuConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if !c.InboundMediaEnabled {
		t.Fatal("expected inbound media enabled by default")
	}
	if !c.InboundReplyContextEnabled {
		t.Fatal("expected inbound reply context enabled by default")
	}
	if c.InboundReplyContextTimeoutSec != defaultInboundReplyContextTimeoutSec {
		t.Fatalf("timeout: %d", c.InboundReplyContextTimeoutSec)
	}
	if c.InboundReplyContextMaxRunes != defaultInboundReplyContextMaxRunes {
		t.Fatalf("max runes: %d", c.InboundReplyContextMaxRunes)
	}
	c2, err := parseFeishuConfig(`{"inbound_reply_context_enabled":false}`)
	if err != nil || c2.InboundReplyContextEnabled {
		t.Fatalf("%+v", c2)
	}
	c3, err := parseFeishuConfig(`{"inbound_media_enabled":false}`)
	if err != nil || c3.InboundMediaEnabled {
		t.Fatalf("%+v", c3)
	}
}

func TestFeishuConfig_inboundStorageRoot(t *testing.T) {
	cfg := FeishuConfig{
		Workspace:      "/tmp/dmr-ws",
		InboundMediaDir: "feishu-inbound",
	}
	root, err := cfg.inboundStorageRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp", "dmr-ws", "feishu-inbound")
	if root != want {
		t.Fatalf("got %q want %q", root, want)
	}
}

func TestFeishuConfig_inboundStorageRoot_rejectsTraversal(t *testing.T) {
	cfg := FeishuConfig{
		Workspace:       "/tmp/dmr-ws",
		InboundMediaDir: "..",
	}
	if _, err := cfg.inboundStorageRoot(); err == nil {
		t.Fatal("expected error")
	}
}

func TestExtractFeishuMessageContent_nonTextBecomesSummary(t *testing.T) {
	raw := `{"file_key":"k","file_name":"x.txt"}`
	msg := &larkim.EventMessage{
		MessageType: ptr(larkim.MsgTypeFile),
		Content:     &raw,
	}
	s := extractFeishuMessageContent(msg)
	if !strings.Contains(s, "feishu_file_key") || !strings.Contains(s, "summary_only") {
		t.Fatal(s)
	}
}
