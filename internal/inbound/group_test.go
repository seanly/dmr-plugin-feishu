package inbound

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestCheckMention(t *testing.T) {
	botOpenID := "ou_bot_12345"

	tests := []struct {
		name        string
		message     *larkim.EventMessage
		botID       string
		wantType    MentionType
		wantIsAtBot bool
		wantIsAtAll bool
	}{
		{
			name:        "nil message",
			message:     nil,
			botID:       botOpenID,
			wantType:    MentionTypeNone,
			wantIsAtBot: false,
			wantIsAtAll: false,
		},
		{
			name:        "no mentions",
			message:     &larkim.EventMessage{},
			botID:       botOpenID,
			wantType:    MentionTypeNone,
			wantIsAtBot: false,
			wantIsAtAll: false,
		},
		{
			name: "bot mentioned",
			message: &larkim.EventMessage{
				Mentions: []*larkim.MentionEvent{
					{
						Id: &larkim.UserId{
							OpenId: strPtr(botOpenID),
						},
						Key: strPtr("@_user_1"),
					},
				},
			},
			botID:       botOpenID,
			wantType:    MentionTypeAtBot,
			wantIsAtBot: true,
			wantIsAtAll: false,
		},
		{
			name: "@all mentioned",
			message: &larkim.EventMessage{
				Mentions: []*larkim.MentionEvent{
					{
						Id:  nil, // @all has nil Id
						Key: strPtr("@_all"),
					},
				},
			},
			botID:       botOpenID,
			wantType:    MentionTypeAtAll,
			wantIsAtBot: false,
			wantIsAtAll: true,
		},
		{
			name: "both bot and @all mentioned",
			message: &larkim.EventMessage{
				Mentions: []*larkim.MentionEvent{
					{
						Id:  nil,
						Key: strPtr("@_all"),
					},
					{
						Id: &larkim.UserId{
							OpenId: strPtr(botOpenID),
						},
						Key: strPtr("@_user_1"),
					},
				},
			},
			botID:       botOpenID,
			wantType:    MentionTypeAtBot, // Bot mention takes precedence over @all
			wantIsAtBot: true,
			wantIsAtAll: false, // When bot is mentioned, Type is AtBot not AtAll
		},
		{
			name: "other user mentioned",
			message: &larkim.EventMessage{
				Mentions: []*larkim.MentionEvent{
					{
						Id: &larkim.UserId{
							OpenId: strPtr("ou_other_user"),
						},
						Key: strPtr("@_user_1"),
					},
				},
			},
			botID:       botOpenID,
			wantType:    MentionTypeNone,
			wantIsAtBot: false,
			wantIsAtAll: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckMention(tt.message, tt.botID)
			if got.Type != tt.wantType {
				t.Errorf("CheckMention() Type = %v, want %v", got.Type, tt.wantType)
			}
			if got.IsAtBot() != tt.wantIsAtBot {
				t.Errorf("CheckMention() IsAtBot = %v, want %v", got.IsAtBot(), tt.wantIsAtBot)
			}
			if got.IsAtAll() != tt.wantIsAtAll {
				t.Errorf("CheckMention() IsAtAll = %v, want %v", got.IsAtAll(), tt.wantIsAtAll)
			}
		})
	}
}

func TestStripMentionText(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		message  *larkim.EventMessage
		expected string
	}{
		{
			name:     "empty text",
			text:     "",
			message:  &larkim.EventMessage{},
			expected: "",
		},
		{
			name:     "no mentions",
			text:     "Hello world",
			message:  &larkim.EventMessage{},
			expected: "Hello world",
		},
		{
			name: "single mention",
			text: "@_user_1 Hello bot",
			message: &larkim.EventMessage{
				Mentions: []*larkim.MentionEvent{
					{Key: strPtr("@_user_1")},
				},
			},
			expected: "Hello bot",
		},
		{
			name: "multiple mentions",
			text: "@_user_1 @_user_2 Hello everyone",
			message: &larkim.EventMessage{
				Mentions: []*larkim.MentionEvent{
					{Key: strPtr("@_user_1")},
					{Key: strPtr("@_user_2")},
				},
			},
			expected: "Hello everyone",
		},
		{
			name: "mention in middle",
			text: "Hello @_user_1 how are you",
			message: &larkim.EventMessage{
				Mentions: []*larkim.MentionEvent{
					{Key: strPtr("@_user_1")},
				},
			},
			expected: "Hello how are you",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripMentionText(tt.text, tt.message)
			if got != tt.expected {
				t.Errorf("StripMentionText(%q) = %q, want %q",
					tt.text, got, tt.expected)
			}
		})
	}
}

func TestGetChatType(t *testing.T) {
	tests := []struct {
		name     string
		chatType *string
		expected string
	}{
		{
			name:     "p2p",
			chatType: strPtr("p2p"),
			expected: "p2p",
		},
		{
			name:     "group",
			chatType: strPtr("group"),
			expected: "group",
		},
		{
			name:     "nil defaults to p2p",
			chatType: nil,
			expected: "p2p",
		},
		{
			name:     "empty defaults to p2p",
			chatType: strPtr(""),
			expected: "p2p",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &larkim.EventMessage{
				ChatType: tt.chatType,
			}
			got := GetChatType(msg)
			if got != tt.expected {
				t.Errorf("GetChatType() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestIsGroupChat(t *testing.T) {
	tests := []struct {
		chatType *string
		expected bool
	}{
		{strPtr("group"), true},
		{strPtr("p2p"), false},
		{nil, false},
		{strPtr(""), false},
	}

	for _, tt := range tests {
		t.Run(StringValue(tt.chatType), func(t *testing.T) {
			msg := &larkim.EventMessage{ChatType: tt.chatType}
			got := IsGroupChat(msg)
			if got != tt.expected {
				t.Errorf("IsGroupChat() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestIsP2PChat(t *testing.T) {
	tests := []struct {
		chatType *string
		expected bool
	}{
		{strPtr("p2p"), true},
		{nil, true},          // nil defaults to p2p
		{strPtr(""), true},   // empty defaults to p2p
		{strPtr("group"), false},
	}

	for _, tt := range tests {
		t.Run(StringValue(tt.chatType), func(t *testing.T) {
			msg := &larkim.EventMessage{ChatType: tt.chatType}
			got := IsP2PChat(msg)
			if got != tt.expected {
				t.Errorf("IsP2PChat() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// Helper function
func strPtr(s string) *string {
	return &s
}
