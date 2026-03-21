package main

import "strings"

// feishuInboundBuiltinSchedulingHint is always prepended for Feishu p2p inbound RunAgent (before optional user extra_prompt).
// It reminds the model that cron-triggered runs need feishu.send_text to reach IM; not configurable to avoid silent mis-delivery.
const feishuInboundBuiltinSchedulingHint = `【飞书·定时】由 dmr-plugin-cron 到点触发的 RunAgent 没有飞书入站上下文，助手最终文本不会自动出现在飞书；若要让定时/提醒内容发到本单聊，须调用 feishu.send_text 并指定 tape_name（与当前会话 tape 一致，例如 feishu:p2p:<chat_id>）。一次性提醒可在 cron.add 中使用 run_once=true。

[Cron/Feishu] Scheduled runs lack Feishu inbound context—assistant text is not auto-posted to IM. To deliver a message here, call feishu.send_text with tape_name matching this chat. For one-shot jobs, use run_once=true on cron.add.`

// feishuInboundBuiltinReportHint tells the model to deliver reports as Markdown files via send_file (better than huge send_text).
const feishuInboundBuiltinReportHint = `【飞书·交付】评估报告、扫描结果、长篇说明等「报告类」输出：请先把正文写成 UTF-8 的 **.md 文件**（可用 fs.write），再调用 **feishu.send_file** 上传该文件，让用户在飞书里打开/下载；不要用超长 **feishu.send_text** 硬塞全文。短回复、一句话结论仍可用 **feishu.send_text**（需要富文本时 markdown=true）。

[Feishu delivery] For reports, assessments, or long write-ups: write Markdown to a **.md** file (e.g. fs.write) then **feishu.send_file** with that path—do not paste the full document into **feishu.send_text**. Brief replies may still use **feishu.send_text** (markdown=true when useful).`

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
