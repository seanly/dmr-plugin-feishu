package inbound

import (
	"testing"
)

func TestStripDMRSubagentChildTapeSuffix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no suffix",
			input:    "feishu:p2p:oc_12345",
			expected: "feishu:p2p:oc_12345",
		},
		{
			name:     "single subagent suffix",
			input:    "feishu:p2p:oc_12345:subagent",
			expected: "feishu:p2p:oc_12345",
		},
		{
			name:     "nested subagent suffix",
			input:    "feishu:p2p:oc_12345:subagent:subagent",
			expected: "feishu:p2p:oc_12345",
		},
		{
			name:     "triple nested subagent",
			input:    "feishu:p2p:oc_12345:subagent:subagent:subagent",
			expected: "feishu:p2p:oc_12345",
		},
		{
			name:     "with whitespace",
			input:    "  feishu:p2p:oc_12345:subagent  ",
			expected: "feishu:p2p:oc_12345",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "only subagent suffix",
			input:    ":subagent",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripDMRSubagentChildTapeSuffix(tt.input)
			if result != tt.expected {
				t.Errorf("StripDMRSubagentChildTapeSuffix(%q) = %q, want %q",
					tt.input, result, tt.expected)
			}
		})
	}
}

func TestP2PChatIDFromTape(t *testing.T) {
	tests := []struct {
		name       string
		tape       string
		wantChatID string
		wantOk     bool
	}{
		{
			name:       "valid p2p tape",
			tape:       "feishu:p2p:oc_12345",
			wantChatID: "oc_12345",
			wantOk:     true,
		},
		{
			name:       "p2p with subagent suffix",
			tape:       "feishu:p2p:oc_12345:subagent",
			wantChatID: "oc_12345",
			wantOk:     true,
		},
		{
			name:       "nested subagent",
			tape:       "feishu:p2p:oc_12345:subagent:subagent",
			wantChatID: "oc_12345",
			wantOk:     true,
		},
		{
			name:       "group tape",
			tape:       "feishu:group:oc_12345:main",
			wantChatID: "",
			wantOk:     false,
		},
		{
			name:       "empty string",
			tape:       "",
			wantChatID: "",
			wantOk:     false,
		},
		{
			name:       "invalid prefix",
			tape:       "web:feishu:p2p:oc_12345",
			wantChatID: "",
			wantOk:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotChatID, gotOk := P2PChatIDFromTape(tt.tape)
			if gotChatID != tt.wantChatID || gotOk != tt.wantOk {
				t.Errorf("P2PChatIDFromTape(%q) = (%q, %v), want (%q, %v)",
					tt.tape, gotChatID, gotOk, tt.wantChatID, tt.wantOk)
			}
		})
	}
}

func TestGroupChatIDFromTape(t *testing.T) {
	tests := []struct {
		name       string
		tape       string
		wantChatID string
		wantOk     bool
	}{
		{
			name:       "valid group main tape",
			tape:       "feishu:group:oc_12345:main",
			wantChatID: "oc_12345",
			wantOk:     true,
		},
		{
			name:       "valid group thread tape",
			tape:       "feishu:group:oc_12345:thread:123",
			wantChatID: "oc_12345",
			wantOk:     true,
		},
		{
			name:       "p2p tape",
			tape:       "feishu:p2p:oc_12345",
			wantChatID: "",
			wantOk:     false,
		},
		{
			name:       "empty string",
			tape:       "",
			wantChatID: "",
			wantOk:     false,
		},
		{
			name:       "invalid chat_id format",
			tape:       "feishu:group:nounderscore:main",
			wantChatID: "",
			wantOk:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotChatID, gotOk := GroupChatIDFromTape(tt.tape)
			if gotChatID != tt.wantChatID || gotOk != tt.wantOk {
				t.Errorf("GroupChatIDFromTape(%q) = (%q, %v), want (%q, %v)",
					tt.tape, gotChatID, gotOk, tt.wantChatID, tt.wantOk)
			}
		})
	}
}

func TestTapeNameForP2P(t *testing.T) {
	tests := []struct {
		chatID   string
		expected string
	}{
		{"oc_12345", "feishu:p2p:oc_12345"},
		{"", "feishu:p2p:"},
		{"oc_abc_xyz", "feishu:p2p:oc_abc_xyz"},
	}

	for _, tt := range tests {
		t.Run(tt.chatID, func(t *testing.T) {
			result := TapeNameForP2P(tt.chatID)
			if result != tt.expected {
				t.Errorf("TapeNameForP2P(%q) = %q, want %q",
					tt.chatID, result, tt.expected)
			}
		})
	}
}

func TestTapeNameForGroup(t *testing.T) {
	tests := []struct {
		chatID   string
		threadID string
		expected string
	}{
		{"oc_12345", "", "feishu:group:oc_12345:main"},
		{"oc_12345", "thread_1", "feishu:group:oc_12345:thread:thread_1"},
	}

	for _, tt := range tests {
		t.Run(tt.chatID+"_"+tt.threadID, func(t *testing.T) {
			result := TapeNameForGroup(tt.chatID, tt.threadID)
			if result != tt.expected {
				t.Errorf("TapeNameForGroup(%q, %q) = %q, want %q",
					tt.chatID, tt.threadID, result, tt.expected)
			}
		})
	}
}


