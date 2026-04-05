// Package utils provides common utility functions for the feishu plugin.
package utils

import "unicode/utf8"

// TruncateRunes truncates a string to maxRunes runes, adding ellipsis if truncated.
func TruncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	if len(runes) > maxRunes {
		s = string(runes[:maxRunes]) + "\n\n…(truncated)"
	}
	return s
}
