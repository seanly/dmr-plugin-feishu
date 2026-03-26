package main

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestShouldSkipRunAgentStandaloneMedia(t *testing.T) {
	if shouldSkipRunAgentStandaloneMedia(nil) {
		t.Fatal("nil")
	}
	pid := "om_parent"
	text := larkim.MsgTypeText
	if shouldSkipRunAgentStandaloneMedia(&larkim.EventMessage{MessageType: &text, ParentId: &pid}) {
		t.Fatal("text with parent should not skip")
	}
	if shouldSkipRunAgentStandaloneMedia(&larkim.EventMessage{MessageType: &text}) {
		t.Fatal("text without parent should not skip")
	}
	img := larkim.MsgTypeImage
	if !shouldSkipRunAgentStandaloneMedia(&larkim.EventMessage{MessageType: &img}) {
		t.Fatal("standalone image should skip")
	}
	if shouldSkipRunAgentStandaloneMedia(&larkim.EventMessage{MessageType: &img, ParentId: &pid}) {
		t.Fatal("image with parent should not skip")
	}
	file := larkim.MsgTypeFile
	if !shouldSkipRunAgentStandaloneMedia(&larkim.EventMessage{MessageType: &file}) {
		t.Fatal("standalone file should skip")
	}
	post := larkim.MsgTypePost
	if shouldSkipRunAgentStandaloneMedia(&larkim.EventMessage{MessageType: &post}) {
		t.Fatal("post should not skip")
	}
}
