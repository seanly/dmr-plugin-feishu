package main

import "testing"

func TestIsAllowedSender(t *testing.T) {
	if !isAllowedSender([]string{"u1", "ou_2"}, "u1") {
		t.Fatal("expected u1 allowed")
	}
	if isAllowedSender([]string{"u1"}, "u2") {
		t.Fatal("expected u2 denied")
	}
	if !isAllowedSender(nil, "anyone") {
		t.Fatal("empty allow list allows all")
	}
}

func TestTapeNameForP2P(t *testing.T) {
	if got := tapeNameForP2P("c1"); got != "feishu:p2p:c1" {
		t.Fatalf("p2p: %s", got)
	}
}

func TestP2PChatIDFromTape(t *testing.T) {
	id, ok := p2pChatIDFromTape("feishu:p2p:abc")
	if !ok || id != "abc" {
		t.Fatalf("got %q %v", id, ok)
	}
	id, ok = p2pChatIDFromTape("feishu:p2p:oc_e7ffa75937d58afcc895f0d2be28497f:subagent")
	if !ok || id != "oc_e7ffa75937d58afcc895f0d2be28497f" {
		t.Fatalf("subagent tape: got %q %v", id, ok)
	}
	_, ok = p2pChatIDFromTape("feishu:group:x:main")
	if ok {
		t.Fatal("expected false")
	}
}
