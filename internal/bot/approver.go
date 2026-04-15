package bot

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// Approval choice values (match dmr plugin.ApprovalChoice / proto).
const (
	ChoiceDenied         int32 = 0
	ChoiceApprovedOnce   int32 = 1
	ChoiceApprovedSess   int32 = 2
	ChoiceApprovedAlways int32 = 3
)

const (
	maxApprovalContentRunesSingle = 8000
	maxApprovalContentRunesBatch  = 2500
	maxApprovalRestJSONRunes      = 6000
)

// ApprovalReply is sent when the user responds in DM.
type ApprovalReply struct {
	Choice  int32
	Indices []int32
	Comment string
}

// ApprovalWait holds the wait state for an approval.
type ApprovalWait struct {
	Ch     chan ApprovalReply
	BatchN int // 0 = single tool approval; >0 = batch size
}

// Approver handles require_approval for private chat.
type Approver struct {
	Plugin interface {
		GetBotForChat(chatID string) (*Instance, error)
	}
	Mu   sync.Mutex
	Wait map[string]*ApprovalWait // p2p chat_id -> wait state
}

// NewApprover creates a new approver for the bot instance.
func NewApprover(plugin interface {
	GetBotForChat(chatID string) (*Instance, error)
}) *Approver {
	return &Approver{
		Plugin: plugin,
		Wait:   make(map[string]*ApprovalWait),
	}
}

// ParseApprovalChoice parses a single approval choice.
func ParseApprovalChoice(content string) (int32, string, bool) {
	choice, comment := splitByCommentMarker(content)
	s := strings.TrimSpace(strings.ToLower(choice))

	if s == "" {
		return ChoiceDenied, comment, true
	}
	if utf8.RuneCountInString(s) == 1 {
		switch s[0] {
		case 'y':
			return ChoiceApprovedOnce, comment, true
		case 's':
			return ChoiceApprovedSess, comment, true
		case 'a':
			return ChoiceApprovedAlways, comment, true
		case 'n':
			return ChoiceDenied, comment, true
		default:
			return ChoiceDenied, comment, true
		}
	}
	switch s {
	case "yes":
		return ChoiceApprovedOnce, comment, true
	case "session":
		return ChoiceApprovedSess, comment, true
	case "always":
		return ChoiceApprovedAlways, comment, true
	case "no":
		return ChoiceDenied, comment, true
	default:
		return ChoiceDenied, comment, true
	}
}

// splitByCommentMarker splits input by "//" into choice and comment.
func splitByCommentMarker(input string) (choice string, comment string) {
	parts := strings.SplitN(input, "//", 2)
	choice = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		comment = strings.TrimSpace(parts[1])
	}
	return
}

// ParseBatchApprovalChoice parses batch approval choice.
func ParseBatchApprovalChoice(content string, total int) (ApprovalReply, bool) {
	choice, comment := splitByCommentMarker(content)
	s := strings.TrimSpace(strings.ToLower(choice))

	if s == "" {
		return ApprovalReply{Choice: ChoiceDenied, Comment: comment}, true
	}
	switch s {
	case "y", "yes":
		return ApprovalReply{Choice: ChoiceApprovedOnce, Comment: comment}, true
	case "s", "session":
		return ApprovalReply{Choice: ChoiceApprovedSess, Comment: comment}, true
	case "a", "always":
		return ApprovalReply{Choice: ChoiceApprovedAlways, Comment: comment}, true
	case "n", "no":
		return ApprovalReply{Choice: ChoiceDenied, Comment: comment}, true
	}
	if strings.Contains(s, ",") || isAllASCIIDigits(s) {
		indices, err := parseApprovalIndices(s, total)
		if err != nil {
			return ApprovalReply{Choice: ChoiceDenied, Comment: comment}, true
		}
		return ApprovalReply{Choice: ChoiceApprovedOnce, Indices: indices, Comment: comment}, true
	}
	return ApprovalReply{Choice: ChoiceDenied, Comment: comment}, true
}

func isAllASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseApprovalIndices parses "1,3,5" into 0-based indices.
func parseApprovalIndices(input string, total int) ([]int32, error) {
	parts := strings.Split(input, ",")
	var indices []int32
	for _, p := range parts {
		p = strings.TrimSpace(p)
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > total {
			return nil, fmt.Errorf("invalid index: %s", p)
		}
		indices = append(indices, int32(n-1))
	}
	if len(indices) == 0 {
		return nil, fmt.Errorf("no indices")
	}
	return indices, nil
}

// TryResolveP2P returns true if the message was consumed as an approval reply.
func (a *Approver) TryResolveP2P(chatID, content string) bool {
	a.Mu.Lock()
	entry := a.Wait[chatID]
	a.Mu.Unlock()
	if entry == nil {
		return false
	}

	var reply ApprovalReply
	if entry.BatchN == 0 {
		c, comment, ok := ParseApprovalChoice(content)
		if !ok {
			return false
		}
		reply = ApprovalReply{Choice: c, Comment: comment}
	} else {
		var ok bool
		reply, ok = ParseBatchApprovalChoice(content, entry.BatchN)
		if !ok {
			return false
		}
	}
	select {
	case entry.Ch <- reply:
	default:
	}
	return true
}

// WaitApproval blocks until the user replies, times out, or context ends.
func (a *Approver) WaitApproval(chatID, prompt string, batchN int, timeout time.Duration, sendFn func(string) error) ApprovalReply {
	ch := make(chan ApprovalReply, 1)

	a.Mu.Lock()
	if _, busy := a.Wait[chatID]; busy {
		a.Mu.Unlock()
		return ApprovalReply{Choice: ChoiceDenied}
	}
	a.Wait[chatID] = &ApprovalWait{Ch: ch, BatchN: batchN}
	a.Mu.Unlock()

	defer func() {
		a.Mu.Lock()
		delete(a.Wait, chatID)
		a.Mu.Unlock()
	}()

	if err := sendFn(prompt); err != nil {
		return ApprovalReply{Choice: ChoiceDenied}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
		return ApprovalReply{Choice: ChoiceDenied}
	}
}

// FormatApprovalArgsMarkdown formats ArgsJSON for display.
func FormatApprovalArgsMarkdown(tool, argsJSON string, contentMaxRunes int) string {
	raw := strings.TrimSpace(argsJSON)
	if raw == "" {
		raw = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		fallback := truncateStringByRunes(raw, maxApprovalRestJSONRunes)
		return "### Arguments\n\n```json\n" + fallback + "\n```\n"
	}

	switch tool {
	case "shell":
		return formatShellArgsMarkdown(args, contentMaxRunes, maxApprovalRestJSONRunes)
	case "fsWrite", "fsEdit":
		return formatFsArgsMarkdown(args, contentMaxRunes, maxApprovalRestJSONRunes)
	default:
		return formatGenericArgsMarkdown(args, contentMaxRunes, maxApprovalRestJSONRunes)
	}
}

func formatShellArgsMarkdown(args map[string]any, cmdMaxRunes, restJSONMaxRunes int) string {
	cmd, _ := args["cmd"].(string)
	delete(args, "cmd")

	var b strings.Builder
	b.WriteString("### Command\n\n```\n")
	b.WriteString(truncateStringByRunes(cmd, cmdMaxRunes))
	b.WriteString("\n```\n")
	if len(args) > 0 {
		b.WriteString("\n")
		b.WriteString(formatRemainingJSONMarkdown(args, restJSONMaxRunes))
	}
	return b.String()
}

func formatFsArgsMarkdown(args map[string]any, contentMaxRunes, restJSONMaxRunes int) string {
	path, _ := args["path"].(string)
	delete(args, "path")

	var b strings.Builder
	if path != "" {
		b.WriteString("### Path\n\n`")
		b.WriteString(path)
		b.WriteString("`\n\n")
	}
	if c, ok := args["content"].(string); ok && c != "" {
		b.WriteString("### File content\n\n")
		b.WriteString(truncateStringByRunes(c, contentMaxRunes))
		b.WriteString("\n\n")
		delete(args, "content")
	}
	if len(args) > 0 {
		b.WriteString(formatRemainingJSONMarkdown(args, restJSONMaxRunes))
	} else if path == "" {
		b.WriteString("*(No path or content in args.)*\n")
	}
	return b.String()
}

func formatGenericArgsMarkdown(args map[string]any, contentMaxRunes, restJSONMaxRunes int) string {
	var b strings.Builder
	if c, ok := args["content"].(string); ok && c != "" {
		b.WriteString("### File content\n\n")
		b.WriteString(truncateStringByRunes(c, contentMaxRunes))
		b.WriteString("\n\n")
		delete(args, "content")
	}
	b.WriteString(formatRemainingJSONMarkdown(args, restJSONMaxRunes))
	return b.String()
}

func formatRemainingJSONMarkdown(args map[string]any, restJSONMaxRunes int) string {
	if len(args) == 0 {
		return ""
	}
	rest, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		rest = []byte("{}")
	}
	rs := string(rest)
	if utf8.RuneCountInString(rs) > restJSONMaxRunes {
		rs = truncateStringByRunes(rs, restJSONMaxRunes)
	}
	return "### Other arguments\n\n```json\n" + rs + "\n```\n"
}

func truncateStringByRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return s
}

// HandleSingle handles single approval request.
func (a *Approver) HandleSingle(req *proto.ApprovalRequest, resp *proto.ApprovalResult, chatID string, sendFn func(string) error, timeout time.Duration) {
	argsStr := strings.TrimSpace(req.ArgsJSON)
	if argsStr == "" {
		argsStr = "{}"
	}
	reason := strings.TrimSpace(req.Decision.Reason)
	risk := strings.TrimSpace(req.Decision.Risk)

	var b strings.Builder
	b.WriteString("## DMR tool approval required\n\n")
	b.WriteString(fmt.Sprintf("- **Tool:** `%s`\n", req.Tool))
	if risk != "" {
		b.WriteString(fmt.Sprintf("- **Risk:** %s\n", risk))
	}
	if reason != "" {
		b.WriteString(fmt.Sprintf("- **Reason:** %s\n", reason))
	}
	b.WriteString("\n")
	b.WriteString(FormatApprovalArgsMarkdown(req.Tool, argsStr, maxApprovalContentRunesSingle))
	b.WriteString("\n### Reply\n\n")
	b.WriteString("Reply with one letter:\n\n")
	b.WriteString("- **y** — approve once\n")
	b.WriteString("- **s** — approve session\n")
	b.WriteString("- **a** — approve always\n")
	b.WriteString("- **n** — deny\n")
	b.WriteString("\n*(Any other reply counts as **deny**.)*\n")
	b.WriteString("\nYou can add a comment after `//`:\n")
	b.WriteString("- Example: `n // security concern`\n")
	b.WriteString("- Example: `y // looks safe`\n")

	body := b.String()
	reply := a.WaitApproval(chatID, body, 0, timeout, sendFn)
	resp.Choice = reply.Choice
	resp.Comment = reply.Comment
	if resp.Choice == ChoiceDenied && resp.Comment == "" {
		resp.Comment = "denied or timeout"
	}
}

// HandleBatch handles batch approval request.
func (a *Approver) HandleBatch(req *proto.BatchApprovalRequest, resp *proto.BatchApprovalResult, chatID string, sendFn func(string) error, timeout time.Duration) {
	if len(req.Requests) == 0 {
		resp.Choice = ChoiceDenied
		return
	}

	first := req.Requests[0]
	reason := strings.TrimSpace(first.Decision.Reason)
	risk := strings.TrimSpace(first.Decision.Risk)
	n := len(req.Requests)

	var b strings.Builder
	b.WriteString("## DMR batch approval\n\n")
	fmt.Fprintf(&b, "**Approval required** — **%d** command(s).\n\n", n)
	if reason != "" {
		b.WriteString(fmt.Sprintf("**Reason:** %s\n\n", reason))
	}
	if risk != "" {
		b.WriteString(fmt.Sprintf("**Risk:** %s\n\n", risk))
	}
	b.WriteString("### Commands\n\n")
	for i, r := range req.Requests {
		if i >= 8 {
			b.WriteString(fmt.Sprintf("\n*(Items after #8 omitted; reply **y** / **1–%d** / **s** / **a** / **n** applies to listed items only.)*\n", n))
			break
		}
		argsStr := strings.TrimSpace(r.ArgsJSON)
		if argsStr == "" {
			argsStr = "{}"
		}
		b.WriteString(formatBatchCommandLine(i+1, r.Tool, argsStr, maxApprovalContentRunesBatch))
		b.WriteString("\n\n")
	}
	b.WriteString("### Reply\n\n")
	b.WriteString("- **y** or **yes** — approve **all** (once)\n")
	fmt.Fprintf(&b, "- **1–%d** or comma-separated (e.g. `1,3,5`) — approve only those items (once)\n", n)
	b.WriteString("- **s** or **session** — allow **all** for this session\n")
	b.WriteString("- **a** or **always** — always allow **all**\n")
	b.WriteString("- **n** or **no** — deny **all**\n")
	b.WriteString("\n*(Any other reply counts as **deny**.)*\n")
	b.WriteString("\nYou can add a comment after `//`:\n")
	b.WriteString("- Example: `n // security concern`\n")
	b.WriteString("- Example: `1,3 // approved, others look risky`\n")

	reply := a.WaitApproval(chatID, b.String(), n, timeout, sendFn)
	resp.Choice = reply.Choice
	resp.Comment = reply.Comment
	if reply.Indices != nil {
		resp.Approved = reply.Indices
	} else {
		resp.Approved = nil
	}
}

// formatBatchCommandLine renders one numbered line.
func formatBatchCommandLine(index int, tool, argsJSON string, contentMaxRunes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%d.** ", index)
	raw := strings.TrimSpace(argsJSON)
	if raw == "" {
		raw = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		fmt.Fprintf(&b, "`%s` — *(invalid args JSON)*\n\n", tool)
		fallback := truncateStringByRunes(raw, 400)
		b.WriteString("```json\n" + fallback + "\n```")
		return b.String()
	}
	switch tool {
	case "shell", "powershell":
		cmd, _ := args["cmd"].(string)
		fmt.Fprintf(&b, "`%s`\n\n", tool)
		b.WriteString("```\n")
		b.WriteString(truncateStringByRunes(cmd, contentMaxRunes))
		b.WriteString("\n```")
		delete(args, "cmd")
		if len(args) > 0 {
			b.WriteString("\n")
			b.WriteString(formatRemainingJSONMarkdown(args, maxApprovalRestJSONRunes))
		}
	case "fsWrite", "fsEdit":
		path, _ := args["path"].(string)
		fmt.Fprintf(&b, "`%s` — path: `%s`", tool, path)
		delete(args, "path")
		if c, ok := args["content"].(string); ok && c != "" {
			b.WriteString("\n\n```\n")
			b.WriteString(truncateStringByRunes(c, contentMaxRunes))
			b.WriteString("\n```")
			delete(args, "content")
		}
		if len(args) > 0 {
			b.WriteString("\n")
			for k, v := range args {
				b.WriteString(fmt.Sprintf("\n- **%s:** `%v`", k, v))
			}
		}
	default:
		fmt.Fprintf(&b, "`%s`", tool)
		for k, v := range args {
			b.WriteString(fmt.Sprintf("\n- **%s:** `%v`", k, v))
		}
	}
	return b.String()
}
