package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const feishuP2PTapePrefix = "feishu:p2p:"

// feishuP2PTapeToChatID returns the chat_id from tape name "feishu:p2p:<chat_id>".
// DMR subagent runs use "feishu:p2p:<chat_id>:subagent"; the ":subagent" suffix is stripped.
func feishuP2PTapeToChatID(tapeName string) (string, error) {
	s := strings.TrimSpace(tapeName)
	if s == "" {
		return "", fmt.Errorf("tape_name is empty")
	}
	if !strings.HasPrefix(s, feishuP2PTapePrefix) {
		return "", fmt.Errorf("tape_name must start with %q (got %q)", feishuP2PTapePrefix, tapeName)
	}
	id := stripDMRSubagentChildTapeSuffix(s[len(feishuP2PTapePrefix):])
	if id == "" {
		return "", fmt.Errorf("tape_name %q has empty chat id after prefix", tapeName)
	}
	return id, nil
}

func sendTextToolParamsJSON() string {
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
				"description": "If true, send as rich post with Markdown (fallback to plain text on API error). If false, send plain text only. Default false.",
			},
			"tape_name": map[string]any{
				"type":        "string",
				"description": "Required when not in a Feishu-triggered RunAgent (e.g. cron). Must be feishu:p2p:<chat_id>, same as DMR tape for that DM. Mutually exclusive with chat_id.",
			},
			"chat_id": map[string]any{
				"type":        "string",
				"description": "Feishu chat_id (e.g. oc_...). Alternative to tape_name when there is no active Feishu inbound job. Mutually exclusive with tape_name.",
			},
		},
	}
	b, _ := json.Marshal(schema)
	return string(b)
}

func argBool(m map[string]any, key string) bool {
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

// execSendText implements feishuSendText for active Feishu jobs and for unattended runs (tape_name / chat_id).
func (p *FeishuPlugin) execSendText(ctx context.Context, argsJSON string) (map[string]any, error) {
	var raw map[string]any
	if strings.TrimSpace(argsJSON) == "" {
		raw = map[string]any{}
	} else if err := json.Unmarshal([]byte(argsJSON), &raw); err != nil {
		return nil, fmt.Errorf("invalid tool arguments JSON: %w", err)
	}

	text := argString(raw, "text")
	if text == "" {
		return nil, fmt.Errorf("text is required")
	}
	text = truncateRunes(text, maxFeishuTextRunes)
	markdown := argBool(raw, "markdown")
	tapeName := argString(raw, "tape_name")
	chatIDArg := argString(raw, "chat_id")

	job := p.getActiveJob()
	if job != nil {
		if tapeName != "" || chatIDArg != "" {
			return nil, fmt.Errorf("do not set tape_name or chat_id during a Feishu-triggered RunAgent; the active private chat is used automatically")
		}
		if err := job.Bot.deliverIMTextForJob(ctx, job, text, markdown); err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":        true,
			"chat_id":   job.ChatID,
			"in_thread": job.InThread,
			"markdown":  markdown,
		}, nil
	}

	if tapeName != "" && chatIDArg != "" {
		return nil, fmt.Errorf("provide at most one of tape_name or chat_id")
	}
	var chatID string
	switch {
	case tapeName != "":
		id, err := feishuP2PTapeToChatID(tapeName)
		if err != nil {
			return nil, err
		}
		chatID = id
	case chatIDArg != "":
		chatID = strings.TrimSpace(chatIDArg)
	default:
		return nil, fmt.Errorf("feishuSendText requires tape_name (e.g. feishu:p2p:<chat_id>) or chat_id when not running from a Feishu-triggered job (e.g. scheduled cron on that tape)")
	}

	bot, err := p.getBotForChat(chatID)
	if err != nil {
		return nil, fmt.Errorf("feishuSendText: %w (chat_id=%q; bot routing is established when the user first messages the bot)", err, chatID)
	}

	if err := bot.deliverIMTextToP2PChat(ctx, chatID, text, markdown); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":       true,
		"chat_id":  chatID,
		"markdown": markdown,
	}, nil
}
