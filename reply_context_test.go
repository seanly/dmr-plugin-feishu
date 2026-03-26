package main

import (
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestLarkMessageToEventMessage(t *testing.T) {
	body := `{"text":"parent hello"}`
	msgType := larkim.MsgTypeText
	mid := "om_parent"
	m := &larkim.Message{
		MessageId: ptr(mid),
		MsgType:   &msgType,
		Body:      &larkim.MessageBody{Content: &body},
	}
	ev := larkMessageToEventMessage(m)
	if ev == nil || stringValue(ev.MessageId) != mid || stringValue(ev.MessageType) != larkim.MsgTypeText {
		t.Fatalf("bad event: %+v", ev)
	}
	if ev.Content == nil || !strings.Contains(*ev.Content, "parent hello") {
		t.Fatalf("content: %v", ev.Content)
	}
}

func TestFormatInboundWithReplyContext_block(t *testing.T) {
	ids := replyContextIDs{
		ParentMessageID:  "om_p",
		CurrentMessageID: "om_c",
		RootMessageID:    "om_r",
	}
	out := formatInboundWithReplyContext(ids, "line one\nline two", "好的。", 10000)
	if !strings.HasPrefix(out, quotedBlockStart) {
		t.Fatal(out)
	}
	if !strings.Contains(out, "parent_message_id: om_p") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "current_message_id: om_c") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "root_message_id: om_r") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "line one") || !strings.HasSuffix(strings.TrimSpace(out), "好的。") {
		t.Fatal(out)
	}
}

func TestFormatInboundWithReplyContext_omitsEmptyRoot(t *testing.T) {
	ids := replyContextIDs{ParentMessageID: "om_p", CurrentMessageID: "om_c"}
	out := formatInboundWithReplyContext(ids, "ctx", "hi", 8000)
	if strings.Contains(out, "root_message_id") {
		t.Fatal(out)
	}
}

func TestFormatInboundWithReplyContext_commaCommandSkipsQuote(t *testing.T) {
	ids := replyContextIDs{ParentMessageID: "om_p", CurrentMessageID: "om_c"}
	out := formatInboundWithReplyContext(ids, "SHOULD NOT APPEAR", ",help", 8000)
	if strings.Contains(out, "SHOULD NOT APPEAR") || strings.Contains(out, quotedBlockStart) {
		t.Fatal(out)
	}
	if out != ",help" {
		t.Fatalf("got %q", out)
	}
}

func TestFormatInboundWithReplyContext_fullWidthComma(t *testing.T) {
	ids := replyContextIDs{ParentMessageID: "om_p"}
	out := formatInboundWithReplyContext(ids, "parent", "，tool", 8000)
	if strings.Contains(out, quotedBlockStart) {
		t.Fatal(out)
	}
	if out != "，tool" {
		t.Fatalf("got %q", out)
	}
}

func TestTruncateReplyContextBody(t *testing.T) {
	s := strings.Repeat("あ", 5)
	out := truncateReplyContextBody(s, 3)
	if !strings.Contains(out, "(truncated)") {
		t.Fatal(out)
	}
	if strings.Count(out, "あ") > 3 {
		t.Fatal(out)
	}
}

func TestIsCommaCommandMessage(t *testing.T) {
	if !isCommaCommandMessage("  ,help") {
		t.Fatal()
	}
	if !isCommaCommandMessage("，x") {
		t.Fatal()
	}
	if isCommaCommandMessage("hello") {
		t.Fatal()
	}
	if isCommaCommandMessage("") {
		t.Fatal()
	}
}

func TestInboundUserContentOrEmptyFallback(t *testing.T) {
	if inboundUserContentOrEmptyFallback("") != "[empty message]" {
		t.Fatal()
	}
	if inboundUserContentOrEmptyFallback("a") != "a" {
		t.Fatal()
	}
}
