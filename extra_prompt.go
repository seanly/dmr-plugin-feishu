package main

import "strings"

// feishuInboundBuiltinSchedulingHint is always prepended for Feishu p2p inbound RunAgent (before optional user extra_prompt).
// It reminds the model that cron-triggered runs need feishuSendText to reach IM; not configurable to avoid silent mis-delivery.
const feishuInboundBuiltinSchedulingHint = `【飞书·定时】由 dmr-plugin-cron 到点触发的 RunAgent 没有飞书入站上下文，助手最终文本不会自动出现在飞书；若要让定时/提醒内容发到本单聊，须调用 feishuSendText 并指定 tape_name（与当前会话 tape 一致，例如 feishu:p2p:<chat_id>）。一次性提醒可在 cronAdd 中使用 run_once=true。

[Cron/Feishu] Scheduled runs lack Feishu inbound context—assistant text is not auto-posted to IM. To deliver a message here, call feishuSendText with tape_name matching this chat. For one-shot jobs, use run_once=true on cronAdd.`

// feishuInboundBuiltinReportHint: all report-style deliverables must go as files (send_file), never as send_text body.
const feishuInboundBuiltinReportHint = `【飞书·报告】凡是报告、分析、总结、评估、扫描结果、巡检说明、多段落技术说明等「报告类」交付：**一律**先把完整正文写成 UTF-8 的 **.md**（或 .txt/.pdf 等合适扩展名，优先 .md）文件（如 **fsWrite**），再 **只** 用 **feishuSendFile** 的 **path** 发到飞书。**禁止**用 **feishuSendText** 发送报告正文（无论长短）；feishuSendText 仅用于非报告类短消息（如一句确认、链接、调度提醒，需要时用 markdown=true）。

[Feishu reports] Any report-style output (analysis, summary, assessment, scan/dump, runbook-style explanation, multi-section write-up): **always** write the full body to a UTF-8 file (prefer **.md**, e.g. fsWrite), then deliver **only** via **feishuSendFile** with **path**. **Do not** put report body in **feishuSendText** (any length). Use **feishuSendText** only for brief non-report messages (ack, link, reminders; markdown=true when useful).`

// composeRunPrompt prefixes built-in hints, then optional user-configured extra (extra_prompt / file), then the inbound user text.
// The combined string is what DMR records as the user tape entry.
func (p *FeishuPlugin) composeRunPrompt(userContent string) string {
	user := userContent
	configExtra := strings.TrimSpace(p.extraRunPrompt)

	var prefixParts []string
	prefixParts = append(prefixParts, strings.TrimSpace(feishuInboundBuiltinSchedulingHint))
	prefixParts = append(prefixParts, strings.TrimSpace(feishuInboundBuiltinReportHint))
	if configExtra != "" {
		prefixParts = append(prefixParts, configExtra)
	}
	prefix := strings.Join(prefixParts, "\n\n")

	if strings.TrimSpace(user) == "" {
		return prefix
	}
	return prefix + "\n\n---\n\nUser message:\n" + user
}
