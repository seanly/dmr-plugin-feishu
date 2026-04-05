package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SendTextParams returns the JSON schema for feishuSendText.
func SendTextParams() string {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"text"},
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "Message body to send. Truncated to Feishu limits if too long.",
			},
			"markdown": map[string]any{
				"type":        "boolean",
				"description": "If true, send as rich post with Markdown. If false, send plain text only. Default false.",
			},
			"tape_name": map[string]any{
				"type":        "string",
				"description": "Required when not in a Feishu-triggered RunAgent. Must be feishu:p2p:<chat_id> or feishu:group:<chat_id>:... Mutually exclusive with chat_id.",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Feishu chat_id. Alternative to tape_name when there is no active Feishu inbound job. Mutually exclusive with tape_name.",
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

// ArgBool extracts a boolean argument from map.
func ArgBool(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "1" || s == "true" || s == "yes"
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	default:
		return false
	}
}

// ArgString extracts a string argument from map.
func ArgString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

// SimpleMessageClient defines the interface for sending messages to a chat.
type SimpleMessageClient interface {
	DeliverIMTextToP2PChat(ctx context.Context, chatID, text string, preferMarkdown bool) error
}

// ThreadAwareMessageClient defines the interface for sending messages with thread context.
type ThreadAwareMessageClient interface {
	SimpleMessageClient
	DeliverIMTextForJob(ctx context.Context, chatID, triggerMessageID string, inThread bool, text string, preferMarkdown bool) error
}

// ExecuteSendText implements feishuSendText.
// When job context exists (jobChatID != ""), it will use thread-aware sending if available.
func ExecuteSendText(ctx context.Context, argsJSON string, jobChatID string, jobTriggerMessageID string, jobInThread bool, jobBot ThreadAwareMessageClient, getBotForChat func(string) (SimpleMessageClient, error)) (map[string]any, error) {
	var raw map[string]any
	if strings.TrimSpace(argsJSON) == "" {
		raw = map[string]any{}
	} else if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil, fmt.Errorf("invalid tool arguments JSON: %w", err)
	}

	text := ArgString(raw, "text")
	if text == "" {
		return nil, fmt.Errorf("text is required")
	}
	markdown := ArgBool(raw, "markdown")

	// If job context exists (Feishu-triggered)
	if jobBot != nil && jobChatID != "" {
		tapeName := ArgString(raw, "tape_name")
		chatIDArg := ArgString(raw, "chat_id")
		if tapeName != "" || chatIDArg != "" {
			return nil, fmt.Errorf("do not set tape_name or chat_id during a Feishu-triggered RunAgent (job is already active)")
		}
		// Use thread-aware sending if in a thread
		if jobInThread && jobTriggerMessageID != "" {
			if err := jobBot.DeliverIMTextForJob(ctx, jobChatID, jobTriggerMessageID, jobInThread, text, markdown); err != nil {
				return nil, err
			}
		} else {
			if err := jobBot.DeliverIMTextToP2PChat(ctx, jobChatID, text, markdown); err != nil {
				return nil, err
			}
		}
		return map[string]any{
			"ok":        true,
			"chat_id":   jobChatID,
			"in_thread": jobInThread,
			"markdown":  markdown,
		}, nil
	}

	// No job context - must provide tape_name or chat_id
	tapeName := ArgString(raw, "tape_name")
	chatIDArg := ArgString(raw, "chat_id")

	if tapeName != "" && chatIDArg != "" {
		return nil, fmt.Errorf("provide at most one of tape_name or chat_id (got both: tape_name=%q, chat_id=%q)", tapeName, chatIDArg)
	}

	var chatID string
	switch {
	case tapeName != "":
		// Extract chat_id from tape_name
		// Supports: feishu:p2p:<chat_id> or feishu:group:<chat_id>:...
		chatID = ExtractChatIDFromTape(tapeName)
		if chatID == "" {
			return nil, fmt.Errorf("tape_name has empty or invalid chat id: %q (expected format: feishu:p2p:<chat_id> or feishu:group:<chat_id>:...)", tapeName)
		}
	case chatIDArg != "":
		chatID = strings.TrimSpace(chatIDArg)
	default:
		return nil, fmt.Errorf("feishuSendText requires tape_name or chat_id when not running from a Feishu-triggered job. " +
			"If calling from a Feishu chat, the job context may have expired. " +
			"Otherwise, provide tape_name (e.g., \"feishu:p2p:oc_xxx\") or chat_id")
	}

	bot, err := getBotForChat(chatID)
	if err != nil {
		return nil, fmt.Errorf("feishuSendText: failed to get bot for chat: %w (chat_id=%q)", err, chatID)
	}

	if err := bot.DeliverIMTextToP2PChat(ctx, chatID, text, markdown); err != nil {
		return nil, fmt.Errorf("feishuSendText: failed to send message: %w", err)
	}
	return map[string]any{
		"ok":       true,
		"chat_id":  chatID,
		"markdown": markdown,
	}, nil
}

// ExtractChatIDFromTape extracts chat_id from various tape name formats.
// Supports:
//   - feishu:p2p:<chat_id>
//   - feishu:p2p:<chat_id>:subagent
//   - feishu:group:<chat_id>:main
//   - feishu:group:<chat_id>:thread:<thread_id>
func ExtractChatIDFromTape(tapeName string) string {
	tapeName = strings.TrimSpace(tapeName)

	// Try p2p format
	const p2pPrefix = "feishu:p2p:"
	if strings.HasPrefix(tapeName, p2pPrefix) {
		tail := tapeName[len(p2pPrefix):]
		// Strip subagent suffix if present
		if idx := strings.Index(tail, ":"); idx > 0 {
			tail = tail[:idx]
		}
		return tail
	}

	// Try group format
	const groupPrefix = "feishu:group:"
	if strings.HasPrefix(tapeName, groupPrefix) {
		tail := tapeName[len(groupPrefix):]
		// Extract chat_id (up to first colon)
		parts := strings.SplitN(tail, ":", 2)
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}

	return ""
}
