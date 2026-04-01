package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// Approval choice values (match dmr plugin.ApprovalChoice / proto).
const (
	choiceDenied         int32 = 0
	choiceApprovedOnce   int32 = 1
	choiceApprovedSess   int32 = 2
	choiceApprovedAlways int32 = 3
)

const (
	maxApprovalContentRunesSingle = 8000
	maxApprovalContentRunesBatch  = 2500
	maxApprovalRestJSONRunes      = 6000
)

// feishuApprovalReply is sent when the user responds in DM.
// For batch: indices == nil means approve all items (y/s/a); non-nil means partial (CLI-style 1,3,5).
type feishuApprovalReply struct {
	choice  int32
	indices []int32
	comment string
}

type approvalWait struct {
	ch     chan feishuApprovalReply
	batchN int // 0 = single tool approval; >0 = batch size (enables 1,3,5 parsing)
}

// FeishuApprover handles require_approval for private chat (feishu:p2p:*) tapes only.
type FeishuApprover struct {
	plugin *FeishuPlugin
	bot    *BotInstance
	mu     sync.Mutex
	wait   map[string]*approvalWait // p2p chat_id -> wait state
}

func newFeishuApprover(p *FeishuPlugin, bot *BotInstance) *FeishuApprover {
	return &FeishuApprover{
		plugin: p,
		bot:    bot,
		wait:   make(map[string]*approvalWait),
	}
}

func parseApprovalChoice(content string) (int32, string, bool) {
	// Split by "//" to separate choice from comment
	choice, comment := splitByCommentMarker(content)
	s := strings.TrimSpace(strings.ToLower(choice))

	if s == "" {
		return choiceDenied, comment, true
	}
	if utf8.RuneCountInString(s) == 1 {
		switch s[0] {
		case 'y':
			return choiceApprovedOnce, comment, true
		case 's':
			return choiceApprovedSess, comment, true
		case 'a':
			return choiceApprovedAlways, comment, true
		case 'n':
			return choiceDenied, comment, true
		default:
			return choiceDenied, comment, true
		}
	}
	switch s {
	case "yes":
		return choiceApprovedOnce, comment, true
	case "session":
		return choiceApprovedSess, comment, true
	case "always":
		return choiceApprovedAlways, comment, true
	case "no":
		return choiceDenied, comment, true
	default:
		return choiceDenied, comment, true
	}
}

// splitByCommentMarker splits input by "//" into choice and comment.
// Examples:
//   "y // all good" -> ("y", "all good")
//   "1,3 // approved" -> ("1,3", "approved")
//   "n" -> ("n", "")
func splitByCommentMarker(input string) (choice string, comment string) {
	parts := strings.SplitN(input, "//", 2)
	choice = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		comment = strings.TrimSpace(parts[1])
	}
	return
}

// parseBatchApprovalChoice mirrors plugins/cliapprover readBatchChoice: y/yes, s/session, a/always, n/no,
// or comma-separated 1-based indices (e.g. 1,3,5). Empty or any other input denies (consumed), same safe default as CLI.
// Also supports comments after "//": "n // security concern" or "1,3 // looks good".
func parseBatchApprovalChoice(content string, total int) (feishuApprovalReply, bool) {
	// Split by "//" to separate choice from comment
	choice, comment := splitByCommentMarker(content)
	s := strings.TrimSpace(strings.ToLower(choice))

	if s == "" {
		return feishuApprovalReply{choice: choiceDenied, comment: comment}, true
	}
	switch s {
	case "y", "yes":
		return feishuApprovalReply{choice: choiceApprovedOnce, comment: comment}, true
	case "s", "session":
		return feishuApprovalReply{choice: choiceApprovedSess, comment: comment}, true
	case "a", "always":
		return feishuApprovalReply{choice: choiceApprovedAlways, comment: comment}, true
	case "n", "no":
		return feishuApprovalReply{choice: choiceDenied, comment: comment}, true
	}
	if strings.Contains(s, ",") || isAllASCIIDigits(s) {
		indices, err := parseApprovalIndices(s, total)
		if err != nil {
			return feishuApprovalReply{choice: choiceDenied, comment: comment}, true
		}
		return feishuApprovalReply{choice: choiceApprovedOnce, indices: indices, comment: comment}, true
	}
	return feishuApprovalReply{choice: choiceDenied, comment: comment}, true
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

// parseApprovalIndices parses "1,3,5" into 0-based indices (CLI semantics).
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
func (a *FeishuApprover) TryResolveP2P(chatID, content string) bool {
	a.mu.Lock()
	entry := a.wait[chatID]
	a.mu.Unlock()
	if entry == nil {
		return false
	}

	var reply feishuApprovalReply
	if entry.batchN == 0 {
		c, comment, ok := parseApprovalChoice(content)
		if !ok {
			return false
		}
		reply = feishuApprovalReply{choice: c, comment: comment}
	} else {
		var ok bool
		reply, ok = parseBatchApprovalChoice(content, entry.batchN)
		if !ok {
			return false
		}
	}
	select {
	case entry.ch <- reply:
	default:
	}
	return true
}

// waitApproval blocks until the user replies, times out, or context ends.
// batchN 0 = single (y/s/a/n only); batchN > 0 = batch (CLI-style yes/no + 1,3,5).
func (a *FeishuApprover) waitApproval(chatID, prompt string, batchN int) feishuApprovalReply {
	timeout := a.plugin.cfg.approvalTimeout()
	ch := make(chan feishuApprovalReply, 1)

	a.mu.Lock()
	if _, busy := a.wait[chatID]; busy {
		a.mu.Unlock()
		return feishuApprovalReply{choice: choiceDenied}
	}
	a.wait[chatID] = &approvalWait{ch: ch, batchN: batchN}
	a.mu.Unlock()

	defer func() {
		a.mu.Lock()
		delete(a.wait, chatID)
		a.mu.Unlock()
	}()

	ctx := a.plugin.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.bot.sendApprovalMessageToChat(ctx, chatID, prompt); err != nil {
		return feishuApprovalReply{choice: choiceDenied}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case v := <-ch:
		return v
	case <-timer.C:
		return feishuApprovalReply{choice: choiceDenied}
	case <-ctx.Done():
		return feishuApprovalReply{choice: choiceDenied}
	}
}

// formatApprovalArgsMarkdown formats ArgsJSON for display; tool-specific layout matches cli_approver.
func formatApprovalArgsMarkdown(tool, argsJSON string, contentMaxRunes int) string {
	raw := strings.TrimSpace(argsJSON)
	if raw == "" {
		raw = "{}"
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		fallback := truncateRunes(raw, maxApprovalRestJSONRunes)
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
	b.WriteString(truncateRunes(cmd, cmdMaxRunes))
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
		b.WriteString(truncateRunes(c, contentMaxRunes))
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
		b.WriteString(truncateRunes(c, contentMaxRunes))
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
		rs = truncateRunes(rs, restJSONMaxRunes)
	}
	return "### Other arguments\n\n```json\n" + rs + "\n```\n"
}

func (a *FeishuApprover) handleSingle(req *proto.ApprovalRequest, resp *proto.ApprovalResult) {
	tape := strings.TrimSpace(req.Tape)
	log.Printf("feishu: approver single tape=%q tool=%q", tape, req.Tool)
	chatID, ok := p2pChatIDFromTape(tape)
	log.Printf("feishu: approver single p2p_parse ok=%v chatID=%q", ok, chatID)
	if !ok {
		resp.Choice = choiceDenied
		resp.Comment = "unknown tape routing for approval"
		return
	}

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
	b.WriteString(formatApprovalArgsMarkdown(req.Tool, argsStr, maxApprovalContentRunesSingle))
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
	reply := a.waitApproval(chatID, body, 0)
	resp.Choice = reply.choice
	resp.Comment = reply.comment
	if resp.Choice == choiceDenied && resp.Comment == "" {
		resp.Comment = "denied or timeout"
	}
}

func (a *FeishuApprover) handleBatch(req *proto.BatchApprovalRequest, resp *proto.BatchApprovalResult) {
	if len(req.Requests) == 0 {
		resp.Choice = choiceDenied
		return
	}
	tape := strings.TrimSpace(req.Requests[0].Tape)
	log.Printf("feishu: approver batch tape=%q reqCount=%d", tape, len(req.Requests))
	for _, r := range req.Requests {
		if strings.TrimSpace(r.Tape) != tape {
			resp.Choice = choiceDenied
			return
		}
	}
	chatID, ok := p2pChatIDFromTape(tape)
	log.Printf("feishu: approver batch p2p_parse ok=%v chatID=%q", ok, chatID)
	if !ok {
		resp.Choice = choiceDenied
		return
	}

	first := req.Requests[0]
	reason := strings.TrimSpace(first.Decision.Reason)
	risk := strings.TrimSpace(first.Decision.Risk)
	n := len(req.Requests)

	var b strings.Builder
	b.WriteString("## DMR batch approval\n\n")
	fmt.Fprintf(&b, "**Approval required** — **%d** command(s) (same layout as CLI approver).\n\n", n)
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

	reply := a.waitApproval(chatID, b.String(), n)
	resp.Choice = reply.choice
	resp.Comment = reply.comment
	if reply.indices != nil {
		resp.Approved = reply.indices
	} else {
		resp.Approved = nil
	}
}

// formatBatchCommandLine renders one numbered line like cliapprover renderToolInfoInline + detail block.
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
		fallback := truncateRunes(raw, 400)
		b.WriteString("```json\n" + fallback + "\n```")
		return b.String()
	}
	switch tool {
	case "shell":
		cmd, _ := args["cmd"].(string)
		fmt.Fprintf(&b, "`%s`\n\n", tool)
		b.WriteString("```\n")
		b.WriteString(truncateRunes(cmd, contentMaxRunes))
		b.WriteString("\n```")
	case "fsWrite", "fsEdit":
		path, _ := args["path"].(string)
		fmt.Fprintf(&b, "`%s` — path: `%s`", tool, path)
		if c, ok := args["content"].(string); ok && c != "" {
			b.WriteString("\n\n```\n")
			b.WriteString(truncateRunes(c, contentMaxRunes))
			b.WriteString("\n```")
		}
	default:
		fmt.Fprintf(&b, "`%s`", tool)
		for k, v := range args {
			b.WriteString(fmt.Sprintf("\n- **%s:** `%v`", k, v))
		}
	}
	return b.String()
}
