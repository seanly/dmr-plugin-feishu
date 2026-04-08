# OPA Policy for Feishu plugin
# Rego V1 format

package dmr

# All feishu operations are allowed by default

decision := {"action": "allow", "reason": "feishu operation", "risk": "low"} if {
	input.tool in [
		"feishuSendFile",
		"feishuSendText"
	]
}
