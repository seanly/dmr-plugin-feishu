package main

import (
	"encoding/json"
	"fmt"
	"strings"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// ParsedInbound summarizes a Feishu IM message for RunAgent user text.
type ParsedInbound struct {
	MsgType     string // lark MsgType* string or empty
	TextBody    string // plain text for msg_type text
	ImageKey    string
	FileKey     string
	FileName    string
	PostPlain   string // best-effort plaintext from post
	RawContent  string // original content JSON when not plain text
	NeedsDownload bool // image or file with a resource key
}

func (p ParsedInbound) resourceKey() string {
	if p.FileKey != "" {
		return p.FileKey
	}
	return p.ImageKey
}

func messageResourceType(parsed ParsedInbound) string {
	if parsed.MsgType == larkim.MsgTypeImage {
		return "image"
	}
	if parsed.MsgType == larkim.MsgTypeFile {
		return "file"
	}
	return ""
}

func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func extractFeishuSenderID(sender *larkim.EventSender) string {
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

func extractFeishuMessageContent(message *larkim.EventMessage) string {
	parsed := parseFeishuInboundMessage(message)
	if parsed.MsgType == larkim.MsgTypeText && parsed.TextBody != "" {
		return parsed.TextBody
	}
	if parsed.MsgType == larkim.MsgTypeText {
		return parsed.RawContent
	}
	return formatInboundSummary(parsed)
}

// parseFeishuInboundMessage classifies msg_type and extracts keys / post text.
func parseFeishuInboundMessage(message *larkim.EventMessage) ParsedInbound {
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
		out.PostPlain = extractPostPlainText([]byte(raw))
	default:
		// leave RawContent
	}
	return out
}

func formatInboundSummary(parsed ParsedInbound) string {
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

func extractPostPlainText(data []byte) string {
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

// dmrSubagentTapeSuffix is appended by DMR core to subagent child tapes (parent + ":subagent").
const dmrSubagentTapeSuffix = ":subagent"

// stripDMRSubagentChildTapeSuffix removes one trailing ":subagent" from the segment after "feishu:p2p:"
// so Feishu receive_id matches the real p2p chat (e.g. oc_...) instead of "oc_...:subagent".
func stripDMRSubagentChildTapeSuffix(tail string) string {
	s := strings.TrimSpace(tail)
	s = strings.TrimSuffix(s, dmrSubagentTapeSuffix)
	return strings.TrimSpace(s)
}

// tapeNameForP2P builds the DMR tape name for private chat (p2p-only plugin).
func tapeNameForP2P(chatID string) string {
	return "feishu:p2p:" + chatID
}

func p2pChatIDFromTape(tape string) (chatID string, ok bool) {
	const p = "feishu:p2p:"
	if !strings.HasPrefix(tape, p) {
		return "", false
	}
	id := stripDMRSubagentChildTapeSuffix(tape[len(p):])
	if id == "" {
		return "", false
	}
	return id, true
}
