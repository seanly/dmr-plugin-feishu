package main

import (
	"strings"
)

// isAllowedSender mirrors picoclaw BaseChannel.IsAllowedSender for simple sender IDs.
func isAllowedSender(allowList []string, senderID string) bool {
	if len(allowList) == 0 {
		return true
	}
	idPart := senderID
	userPart := ""
	if idx := strings.Index(senderID, "|"); idx > 0 {
		idPart = senderID[:idx]
		userPart = senderID[idx+1:]
	}
	for _, allowed := range allowList {
		trimmed := strings.TrimPrefix(allowed, "@")
		allowedID := trimmed
		allowedUser := ""
		if idx := strings.Index(trimmed, "|"); idx > 0 {
			allowedID = trimmed[:idx]
			allowedUser = trimmed[idx+1:]
		}
		if senderID == allowed ||
			idPart == allowed ||
			senderID == trimmed ||
			idPart == trimmed ||
			(strings.EqualFold(userPart, allowedUser) && allowedUser != "") {
			return true
		}
		if strings.EqualFold(strings.TrimPrefix(idPart, "@"), strings.TrimPrefix(allowedID, "@")) {
			return true
		}
	}
	return false
}
