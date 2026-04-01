package main

import (
	"context"
	"strings"
	"testing"
)

func TestFeishuP2PTapeToChatID(t *testing.T) {
	id, err := feishuP2PTapeToChatID("feishu:p2p:oc_abc123")
	if err != nil || id != "oc_abc123" {
		t.Fatalf("got %q err=%v", id, err)
	}
	id, err = feishuP2PTapeToChatID("feishu:p2p:oc_abc123:subagent")
	if err != nil || id != "oc_abc123" {
		t.Fatalf("subagent suffix: got %q err=%v", id, err)
	}
	_, err = feishuP2PTapeToChatID("")
	if err == nil {
		t.Fatal("expected error for empty")
	}
	_, err = feishuP2PTapeToChatID("feishu:group:x")
	if err == nil || !strings.Contains(err.Error(), "feishu:p2p:") {
		t.Fatalf("got %v", err)
	}
	_, err = feishuP2PTapeToChatID("feishu:p2p:")
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("got %v", err)
	}
}

func TestArgBool(t *testing.T) {
	if !argBool(map[string]any{"x": true}, "x") {
		t.Fatal("true")
	}
	if argBool(map[string]any{"x": false}, "x") {
		t.Fatal("false")
	}
	if !argBool(map[string]any{"x": "true"}, "x") {
		t.Fatal("string true")
	}
	if !argBool(map[string]any{"x": float64(1)}, "x") {
		t.Fatal("float 1")
	}
	if argBool(map[string]any{}, "missing") {
		t.Fatal("missing")
	}
}

func TestExecSendText_missingText(t *testing.T) {
	p := NewFeishuPlugin()
	_, err := p.execSendText(context.Background(), `{}`)
	if err == nil || !strings.Contains(err.Error(), "text") {
		t.Fatalf("got %v", err)
	}
}

func TestExecSendText_noJobNoTarget(t *testing.T) {
	p := NewFeishuPlugin()
	_, err := p.execSendText(context.Background(), `{"text":"hi"}`)
	if err == nil || !strings.Contains(err.Error(), "tape_name") {
		t.Fatalf("got %v", err)
	}
}

func TestExecSendText_noJobBothTargets(t *testing.T) {
	p := NewFeishuPlugin()
	_, err := p.execSendText(context.Background(), `{"text":"hi","tape_name":"feishu:p2p:oc_x","chat_id":"oc_y"}`)
	if err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Fatalf("got %v", err)
	}
}

func TestExecSendText_activeJobWithTapeNameRejected(t *testing.T) {
	p := NewFeishuPlugin()
	p.setActiveJob(&inboundJob{ChatID: "oc_x"})
	defer p.clearActiveJob()

	_, err := p.execSendText(context.Background(), `{"text":"hi","tape_name":"feishu:p2p:oc_x"}`)
	if err == nil || !strings.Contains(err.Error(), "tape_name") {
		t.Fatalf("got %v", err)
	}
}

func TestExecSendText_noClientWithTarget(t *testing.T) {
	p := NewFeishuPlugin()
	// Register a bot with nil lc for the chat
	bot := &BotInstance{}
	p.registerChatRoute("oc_1", bot)
	// Valid args but lc is nil -> deliver fails
	_, err := p.execSendText(context.Background(), `{"text":"hi","tape_name":"feishu:p2p:oc_1"}`)
	if err == nil || !strings.Contains(err.Error(), "client not initialized") {
		t.Fatalf("got %v", err)
	}
}

func TestExecSendText_activeJobNoClient(t *testing.T) {
	p := NewFeishuPlugin()
	bot := &BotInstance{} // nil lc
	p.setActiveJob(&inboundJob{ChatID: "oc_x", Bot: bot})
	defer p.clearActiveJob()

	_, err := p.execSendText(context.Background(), `{"text":"hello"}`)
	if err == nil || !strings.Contains(err.Error(), "client not initialized") {
		t.Fatalf("got %v", err)
	}
}
