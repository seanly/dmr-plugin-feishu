package inbound

import (
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// MentionType represents how the bot is mentioned in a group message.
type MentionType int

const (
	MentionTypeNone MentionType = iota
	MentionTypeAtBot
	MentionTypeAtAll
)

// MentionInfo represents mention information parsed from a message.
type MentionInfo struct {
	Type   MentionType
	BotIDs []string // list of mentioned bot open_ids
}

// CheckMention checks if and how the bot is mentioned in a message.
// It returns:
//   - MentionTypeAtBot: if the bot (by botID) is @mentioned
//   - MentionTypeAtAll: if @all is used (but bot is not specifically mentioned)
//   - MentionTypeNone: if bot is not mentioned
func CheckMention(message *larkim.EventMessage, botID string) MentionInfo {
	info := MentionInfo{Type: MentionTypeNone}

	if message == nil {
		return info
	}

	// Check mentions list
	for _, m := range message.Mentions {
		_, openID, isAtAll := extractMentionEvent(m)

		// @all detection
		if isAtAll {
			info.Type = MentionTypeAtAll
		}
		// Bot mention detection
		if openID != "" && openID == botID && botID != "" {
			info.Type = MentionTypeAtBot
			info.BotIDs = append(info.BotIDs, openID)
		}
	}

	return info
}

// extractMentionEvent extracts mention information from SDK MentionEvent.
// Returns the key (placeholder), the open_id (if available), and a flag indicating if it's @all.
func extractMentionEvent(m *larkim.MentionEvent) (key, openID string, isAtAll bool) {
	if m == nil {
		return "", "", false
	}
	if m.Key != nil {
		key = *m.Key
	}
	// Check if this is @all - in Feishu, @all has a special structure
	// When @all, Id may be nil or have special values
	if m.Id == nil {
		// This could be @all
		isAtAll = true
		return key, "", true
	}
	// Get open_id from UserId struct
	if m.Id.OpenId != nil {
		openID = *m.Id.OpenId
	}
	return key, openID, false
}

// IsAtAll returns true if the message contains @all.
func (m MentionInfo) IsAtAll() bool {
	return m.Type == MentionTypeAtAll
}

// IsAtBot returns true if the specific bot is mentioned.
func (m MentionInfo) IsAtBot() bool {
	return m.Type == MentionTypeAtBot
}

// TapeNameForGroup builds the DMR tape name for group chat.
// Format: feishu:group:<chat_id>:main (for non-thread messages)
// Format: feishu:group:<chat_id>:thread:<thread_id> (for thread messages)
func TapeNameForGroup(chatID, threadID string) string {
	if threadID == "" {
		return "feishu:group:" + chatID + ":main"
	}
	return "feishu:group:" + chatID + ":thread:" + threadID
}

// QueueKeyForGroup builds the queue key for group chat.
// For parallel processing per thread: each thread gets its own queue.
func QueueKeyForGroup(chatID, threadID string) string {
	return TapeNameForGroup(chatID, threadID)
}

// GroupChatInfo holds extracted group chat information.
type GroupChatInfo struct {
	ChatID   string
	ThreadID string
	InThread bool
	ChatType string
}

// ExtractGroupChatInfo extracts group chat info from a message.
func ExtractGroupChatInfo(message *larkim.EventMessage) GroupChatInfo {
	info := GroupChatInfo{
		ChatID:   StringValue(message.ChatId),
		ThreadID: StringValue(message.ThreadId),
		ChatType: StringValue(message.ChatType),
	}
	info.InThread = info.ThreadID != ""
	return info
}

// StripMentionText removes @bot mentions from message text content.
// This cleans up the text before sending to the agent.
func StripMentionText(text string, message *larkim.EventMessage) string {
	if text == "" || message == nil || len(message.Mentions) == 0 {
		return text
	}

	result := text
	for _, m := range message.Mentions {
		key, _, _ := extractMentionEvent(m)
		// Replace the mention placeholder like "@_user_1" with empty string
		if key != "" {
			result = strings.ReplaceAll(result, key, "")
		}
	}

	// Clean up extra whitespace
	result = strings.Join(strings.Fields(result), " ")
	return strings.TrimSpace(result)
}

// IsAtAll returns true if the message contains @all.
func IsAtAll(message *larkim.EventMessage) bool {
	if message == nil {
		return false
	}
	for _, m := range message.Mentions {
		_, _, isAtAll := extractMentionEvent(m)
		if isAtAll {
			return true
		}
	}
	return false
}

// GetChatType returns the chat type from message, normalizing empty to "p2p".
func GetChatType(message *larkim.EventMessage) string {
	ct := StringValue(message.ChatType)
	if ct == "" {
		// Default to p2p for backward compatibility
		return "p2p"
	}
	return ct
}

// IsGroupChat returns true if this is a group chat message.
func IsGroupChat(message *larkim.EventMessage) bool {
	return GetChatType(message) == "group"
}

// IsP2PChat returns true if this is a private chat message.
func IsP2PChat(message *larkim.EventMessage) bool {
	ct := GetChatType(message)
	return ct == "p2p" || ct == ""
}
