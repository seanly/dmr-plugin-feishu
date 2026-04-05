package inbound

import (
	"encoding/json"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// ParsedInbound summarizes a Feishu IM message for RunAgent user text.
type ParsedInbound struct {
	MsgType       string // lark MsgType* string or empty
	TextBody      string // plain text for msg_type text
	ImageKey      string
	FileKey       string
	FileName      string
	PostPlain     string // best-effort plaintext from post
	RawContent    string // original content JSON when not plain text
	NeedsDownload bool   // image or file with a resource key
}

// ResourceKey returns file_key if set, otherwise image_key.
func (p ParsedInbound) ResourceKey() string {
	if p.FileKey != "" {
		return p.FileKey
	}
	return p.ImageKey
}

// MessageResourceType returns the resource type for download.
func MessageResourceType(parsed ParsedInbound) string {
	if parsed.MsgType == larkim.MsgTypeImage {
		return "image"
	}
	if parsed.MsgType == larkim.MsgTypeFile {
		return "file"
	}
	return ""
}

// StringValue safely dereferences a string pointer.
func StringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ExtractFeishuSenderID extracts sender ID from event.
func ExtractFeishuSenderID(sender *larkim.EventSender) string {
	if sender == nil || sender.SenderId == nil {
		return ""
	}
	if sender.SenderId.UserId != nil && *sender.SenderId.UserId != "" {
		return *sender.SenderId.UserId
	}
	if sender.SenderId.OpenId != nil && *sender.SenderId.OpenId != "" {
		return *sender.SenderId.OpenId
	}
	if sender.SenderId.UnionId != nil && *sender.SenderId.UnionId != "" {
		return *sender.SenderId.UnionId
	}
	return ""
}

// ExtractFeishuMessageContent extracts display content from message.
func ExtractFeishuMessageContent(message *larkim.EventMessage) string {
	parsed := ParseFeishuInboundMessage(message)
	if parsed.MsgType == larkim.MsgTypeText && parsed.TextBody != "" {
		return parsed.TextBody
	}
	if parsed.MsgType == larkim.MsgTypeText {
		return parsed.RawContent
	}
	return FormatInboundSummary(parsed)
}

// ParseFeishuInboundMessage classifies msg_type and extracts keys / post text.
func ParseFeishuInboundMessage(message *larkim.EventMessage) ParsedInbound {
	var out ParsedInbound
	if message == nil || message.Content == nil {
		return out
	}
	raw := strings.TrimSpace(*message.Content)
	out.RawContent = raw
	if raw == "" {
		return out
	}
	mt := ""
	if message.MessageType != nil {
		mt = *message.MessageType
	}
	out.MsgType = mt

	switch mt {
	case larkim.MsgTypeText:
		var textPayload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(raw), &textPayload); err == nil {
			out.TextBody = textPayload.Text
		}
	case larkim.MsgTypeImage:
		var img struct {
			ImageKey string `json:"image_key"`
		}
		if err := json.Unmarshal([]byte(raw), &img); err == nil {
			out.ImageKey = strings.TrimSpace(img.ImageKey)
		}
		out.NeedsDownload = out.ImageKey != ""
	case larkim.MsgTypeFile:
		var f struct {
			FileKey  string `json:"file_key"`
			FileName string `json:"file_name"`
		}
		if err := json.Unmarshal([]byte(raw), &f); err == nil {
			out.FileKey = strings.TrimSpace(f.FileKey)
			out.FileName = strings.TrimSpace(f.FileName)
		}
		out.NeedsDownload = out.FileKey != ""
	case larkim.MsgTypePost:
		out.PostPlain = ExtractPostPlainText([]byte(raw))
	default:
		// leave RawContent
	}
	return out
}

// FormatInboundSummary formats parsed message as human-readable summary.
func FormatInboundSummary(parsed ParsedInbound) string {
	if parsed.MsgType == larkim.MsgTypePost {
		s := "[Feishu inbound — msg_type=post]\n"
		if parsed.PostPlain != "" {
			s += parsed.PostPlain
		}
		return strings.TrimSpace(s)
	}
	if parsed.MsgType == "" && parsed.RawContent != "" {
		return fmt.Sprintf("[Feishu inbound — unknown or empty msg_type]\n%s", parsed.RawContent)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[Feishu inbound]\nmsg_type: %s\n", parsed.MsgType)
	switch parsed.MsgType {
	case larkim.MsgTypeImage:
		if parsed.ImageKey != "" {
			fmt.Fprintf(&b, "feishu_image_key: %s\n", parsed.ImageKey)
		}
	case larkim.MsgTypeFile:
		if parsed.FileKey != "" {
			fmt.Fprintf(&b, "feishu_file_key: %s\n", parsed.FileKey)
		}
		if parsed.FileName != "" {
			fmt.Fprintf(&b, "file_name: %s\n", parsed.FileName)
		}
	default:
		if parsed.RawContent != "" {
			fmt.Fprintf(&b, "content_json: %s\n", parsed.RawContent)
		}
	}
	b.WriteString("\nstatus: summary_only\n")
	if parsed.NeedsDownload {
		b.WriteString("hint: set inbound_media_enabled:true to download into workspace and receive local_path.\n")
	}
	return strings.TrimSpace(b.String())
}

// ExtractPostPlainText extracts plain text from a post message.
func ExtractPostPlainText(data []byte) string {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return ""
	}
	var b strings.Builder
	walkJSONForText(v, &b)
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}

func walkJSONForText(v any, b *strings.Builder) {
	switch x := v.(type) {
	case map[string]any:
		if tag, ok := x["tag"].(string); ok && tag == "text" {
			if t, ok := x["text"].(string); ok && t != "" {
				b.WriteString(t)
				b.WriteByte(' ')
			}
		}
		for _, vv := range x {
			walkJSONForText(vv, b)
		}
	case []any:
		for _, vv := range x {
			walkJSONForText(vv, b)
		}
	}
}

// DMRSubagentTapeSuffix is appended by DMR core to subagent child tapes (parent + ":subagent").
const DMRSubagentTapeSuffix = ":subagent"

// StripDMRSubagentChildTapeSuffix removes all trailing ":subagent" suffixes recursively.
// This handles nested subagents: "feishu:p2p:xxx:subagent:subagent" -> "feishu:p2p:xxx"
func StripDMRSubagentChildTapeSuffix(tail string) string {
	s := strings.TrimSpace(tail)
	// Recursively remove all :subagent suffixes to handle nested subagents
	for strings.HasSuffix(s, DMRSubagentTapeSuffix) {
		s = strings.TrimSuffix(s, DMRSubagentTapeSuffix)
		s = strings.TrimSpace(s)
	}
	return s
}

// TapeNameForP2P builds the DMR tape name for private chat (p2p-only plugin).
func TapeNameForP2P(chatID string) string {
	return "feishu:p2p:" + chatID
}

// P2PChatIDFromTape extracts chat_id from tape name.
func P2PChatIDFromTape(tape string) (chatID string, ok bool) {
	const p = "feishu:p2p:"
	if !strings.HasPrefix(tape, p) {
		return "", false
	}
	id := StripDMRSubagentChildTapeSuffix(tape[len(p):])
	if id == "" {
		return "", false
	}
	return id, true
}

// FeishuP2PTapeToChatID returns the chat_id from tape name "feishu:p2p:<chat_id>".
// DMR subagent runs use "feishu:p2p:<chat_id>:subagent"; the ":subagent" suffix is stripped.
func FeishuP2PTapeToChatID(tapeName string) (string, error) {
	s := strings.TrimSpace(tapeName)
	if s == "" {
		return "", fmt.Errorf("tape_name is empty")
	}
	const prefix = "feishu:p2p:"
	if !strings.HasPrefix(s, prefix) {
		return "", fmt.Errorf("tape_name must start with %q (got %q)", prefix, tapeName)
	}
	id := StripDMRSubagentChildTapeSuffix(s[len(prefix):])
	if id == "" {
		return "", fmt.Errorf("tape_name %q has empty chat id after prefix", tapeName)
	}
	return id, nil
}

// GroupChatIDFromTape extracts chat_id from group tape names.
// Supports formats:
//   - feishu:group:<chat_id>:main
//   - feishu:group:<chat_id>:thread:<thread_id>
func GroupChatIDFromTape(tape string) (string, bool) {
	s := strings.TrimSpace(tape)
	const prefix = "feishu:group:"
	if !strings.HasPrefix(s, prefix) {
		return "", false
	}

	// Remove prefix
	tail := s[len(prefix):]

	// Extract chat_id (up to first colon)
	parts := strings.SplitN(tail, ":", 2)
	if len(parts) == 0 || parts[0] == "" {
		return "", false
	}

	chatID := parts[0]

	// Verify it looks like a valid chat_id (oc_*, og_*, etc.)
	if !strings.Contains(chatID, "_") {
		return "", false
	}

	return chatID, true
}
