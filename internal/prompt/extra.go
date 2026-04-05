package prompt

import (
	"os"
	"path/filepath"
	"strings"
)

// FeishuInboundBuiltinSchedulingHint is always prepended for Feishu p2p inbound RunAgent.
const FeishuInboundBuiltinSchedulingHint = `【飞书·定时】在本会话使用 cronAdd 时不要传 tape_name，任务绑定当前会话 tape。由 dmr-plugin-cron 到点触发的 RunAgent 没有飞书入站上下文，助手最终文本不会自动出现在飞书；若要让定时/提醒内容发到本单聊，须调用 feishuSendText 并指定 tape_name（与当前会话一致）。一次性提醒可在 cronAdd 中使用 run_once=true。

[Cron/Feishu] In this chat, cronAdd does not take tape_name—the job uses the current session tape. Scheduled runs lack Feishu inbound context—assistant text is not auto-posted to IM; use feishuSendText with tape_name matching this chat to deliver. For one-shot jobs, use run_once=true on cronAdd.`

// FeishuInboundBuiltinReportHint: all report-style deliverables must go as files.
const FeishuInboundBuiltinReportHint = `【飞书·报告】凡是报告、分析、总结、评估、扫描结果、巡检说明、多段落技术说明等「报告类」交付：**一律**先把完整正文写成 UTF-8 的 **.md**（或 .txt/.pdf 等合适扩展名，优先 .md）文件（如 **fsWrite**），再 **只** 用 **feishuSendFile** 的 **path** 发到飞书。**禁止**用 **feishuSendText** 发送报告正文（无论长短）；feishuSendText 仅用于非报告类短消息（如一句确认、链接、调度提醒，需要时用 markdown=true）。

[Feishu reports] Any report-style output (analysis, summary, assessment, scan/dump, runbook-style explanation, multi-section write-up): **always** write the full body to a UTF-8 file (prefer **.md**, e.g. fsWrite), then deliver **only** via **feishuSendFile** with **path**. **Do not** put report body in **feishuSendText** (any length). Use **feishuSendText** only for brief non-report messages (ack, link, reminders; markdown=true when useful).`

// Composer builds the run prompt with built-in hints and optional extra content.
type Composer struct {
	ExtraRunPrompt string
}

// NewComposer creates a new prompt composer.
func NewComposer(extraRunPrompt string) *Composer {
	return &Composer{ExtraRunPrompt: extraRunPrompt}
}

// Compose builds the final prompt with built-in hints + optional extra + user content.
func (c *Composer) Compose(userContent string) string {
	user := userContent
	configExtra := strings.TrimSpace(c.ExtraRunPrompt)

	var prefixParts []string
	prefixParts = append(prefixParts, strings.TrimSpace(FeishuInboundBuiltinSchedulingHint))
	prefixParts = append(prefixParts, strings.TrimSpace(FeishuInboundBuiltinReportHint))
	if configExtra != "" {
		prefixParts = append(prefixParts, configExtra)
	}
	prefix := strings.Join(prefixParts, "\n\n")

	if strings.TrimSpace(user) == "" {
		return prefix
	}
	return prefix + "\n\n---\n\nUser message:\n" + user
}

// ResolveExtraPromptPath resolves path for extra_prompt_file: absolute as-is, else join with config_base_dir.
func ResolveExtraPromptPath(path, configBaseDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	base := strings.TrimSpace(configBaseDir)
	if base != "" {
		return filepath.Clean(filepath.Join(base, path))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return abs
}

// BuildResolvedExtraPrompt loads file (if set) then appends ExtraPrompt.
// Order: file body, blank line, inline extra_prompt.
func BuildResolvedExtraPrompt(extraPromptFile, extraPrompt, configBaseDir string) (string, error) {
	var parts []string
	if fp := strings.TrimSpace(extraPromptFile); fp != "" {
		abs := ResolveExtraPromptPath(fp, configBaseDir)
		b, err := os.ReadFile(abs)
		if err != nil {
			return "", err
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			parts = append(parts, s)
		}
	}
	if s := strings.TrimSpace(extraPrompt); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n"), nil
}
