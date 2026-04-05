package inbound

import "strings"

// IsAllowedSender checks if sender is in allowed list (empty list allows all).
func IsAllowedSender(allowList []string, senderID string) bool {
	if len(allowList) == 0 {
		return true
	}
	for _, id := range allowList {
		if strings.EqualFold(strings.TrimSpace(id), senderID) {
			return true
		}
	}
	return false
}
