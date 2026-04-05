package plugin

import (
	"testing"
)

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
		wantErr bool
		check   func(t *testing.T, cfg Config)
	}{
		{
			name:    "empty config uses defaults",
			jsonStr: "",
			wantErr: false,
			check: func(t *testing.T, cfg Config) {
				if cfg.ApprovalTimeoutSec != 300 {
					t.Errorf("ApprovalTimeoutSec = %d, want 300", cfg.ApprovalTimeoutSec)
				}
				if cfg.DedupTTLMinutes != 10 {
					t.Errorf("DedupTTLMinutes = %d, want 10", cfg.DedupTTLMinutes)
				}
				if !cfg.InboundMediaEnabledVal {
					t.Error("InboundMediaEnabledVal should be true by default")
				}
				if cfg.GetSendFileMaxBytes() != defaultSendFileMaxBytes {
					t.Errorf("SendFileMaxBytes = %d, want %d", cfg.GetSendFileMaxBytes(), defaultSendFileMaxBytes)
				}
			},
		},
		{
			name:    "valid config",
			jsonStr: `{"approval_timeout_sec": 600, "dedup_ttl_minutes": 20}`,
			wantErr: false,
			check: func(t *testing.T, cfg Config) {
				if cfg.ApprovalTimeoutSec != 600 {
					t.Errorf("ApprovalTimeoutSec = %d, want 600", cfg.ApprovalTimeoutSec)
				}
				if cfg.DedupTTLMinutes != 20 {
					t.Errorf("DedupTTLMinutes = %d, want 20", cfg.DedupTTLMinutes)
				}
			},
		},
		{
			name:    "backward compat single bot",
			jsonStr: `{"app_id": "cli_xxx", "app_secret": "secret", "verification_token": "token", "encrypt_key": "key"}`,
			wantErr: false,
			check: func(t *testing.T, cfg Config) {
				if len(cfg.Bots) != 1 {
					t.Fatalf("Expected 1 bot, got %d", len(cfg.Bots))
				}
				if cfg.Bots[0].AppID != "cli_xxx" {
					t.Errorf("Bot AppID = %q, want cli_xxx", cfg.Bots[0].AppID)
				}
			},
		},
		{
			name:    "multi-bot config",
			jsonStr: `{"bots": [{"app_id": "cli_1"}, {"app_id": "cli_2"}]}`,
			wantErr: false,
			check: func(t *testing.T, cfg Config) {
				if len(cfg.Bots) != 2 {
					t.Fatalf("Expected 2 bots, got %d", len(cfg.Bots))
				}
				if cfg.Bots[0].AppID != "cli_1" {
					t.Errorf("Bot[0] AppID = %q, want cli_1", cfg.Bots[0].AppID)
				}
				if cfg.Bots[1].AppID != "cli_2" {
					t.Errorf("Bot[1] AppID = %q, want cli_2", cfg.Bots[1].AppID)
				}
			},
		},
		{
			name:    "multi-bot takes precedence over legacy",
			jsonStr: `{"app_id": "legacy_id", "bots": [{"app_id": "new_id"}]}`,
			wantErr: false,
			check: func(t *testing.T, cfg Config) {
				if len(cfg.Bots) != 1 {
					t.Fatalf("Expected 1 bot, got %d", len(cfg.Bots))
				}
				if cfg.Bots[0].AppID != "new_id" {
					t.Errorf("Bot AppID = %q, want new_id", cfg.Bots[0].AppID)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseConfig(tt.jsonStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseConfig() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		wantErr  bool
	}{
		{"100", 100, false},
		{"100B", 100, false},
		{"10K", 10 * 1024, false},
		{"10KB", 10 * 1024, false},
		{"5M", 5 * 1024 * 1024, false},
		{"5MB", 5 * 1024 * 1024, false},
		{"1G", 1 * 1024 * 1024 * 1024, false},
		{"1GB", 1 * 1024 * 1024 * 1024, false},
		{"1.5GB", int64(1.5 * 1024 * 1024 * 1024), false},
		{"", 0, false},
		{"invalid", 0, true},
		{"10XB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("ParseSize(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ApprovalTimeoutSec != 300 {
		t.Errorf("ApprovalTimeoutSec = %d, want 300", cfg.ApprovalTimeoutSec)
	}

	if cfg.DedupTTLMinutes != 10 {
		t.Errorf("DedupTTLMinutes = %d, want 10", cfg.DedupTTLMinutes)
	}

	if !cfg.InboundMediaEnabledVal {
		t.Error("InboundMediaEnabledVal should be true")
	}

	if !cfg.InboundReplyContextEnabled {
		t.Error("InboundReplyContextEnabled should be true")
	}
}
