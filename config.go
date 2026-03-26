package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultSendFileMaxBytes              = 30 * 1024 * 1024 // 30 MiB
	defaultInboundMediaDir               = "feishu-inbound"
	defaultInboundMediaTimeoutSec        = 45
	defaultInboundReplyContextTimeoutSec = 12
	defaultInboundReplyContextMaxRunes   = 8000
)

// FeishuConfig is loaded from the plugin InitRequest.ConfigJSON (YAML becomes map then JSON in DMR).
type FeishuConfig struct {
	// ConfigBaseDir is injected by DMR (absolute directory of the main config file). Used to resolve relative extra_prompt_file paths.
	ConfigBaseDir string `json:"config_base_dir"`
	// Workspace is injected by DMR (same absolute path as fs/shell tools). Used for inbound media storage.
	Workspace string `json:"workspace"`
	AppID               string   `json:"app_id"`
	AppSecret           string   `json:"app_secret"`
	VerificationToken   string   `json:"verification_token"`
	EncryptKey          string   `json:"encrypt_key"`
	AllowFrom          []string `json:"allow_from"`
	ApprovalTimeoutSec int      `json:"approval_timeout_sec"`
	DedupTTLMinutes     int      `json:"dedup_ttl_minutes"`
	// SendFileMaxBytes caps feishuSendFile uploads (default 30 MiB).
	SendFileMaxBytes int `json:"send_file_max_bytes"`
	// SendFileRoot if set, path arguments must resolve under this directory (absolute recommended).
	SendFileRoot string `json:"send_file_root"`
	// ExtraPrompt is appended after ExtraPromptFile content (if any) and prefixed to each Feishu inbound RunAgent user message. See README (not applied to cron-only RunAgent).
	ExtraPrompt string `json:"extra_prompt"`
	// ExtraPromptFile is UTF-8 text; relative paths resolve against ConfigBaseDir.
	ExtraPromptFile string `json:"extra_prompt_file"`
	// DmrRestartTrigger + DmrRestartToken: if token is non-empty, a p2p message whose first line is
	// "<trigger> <token>" triggers host RestartHost (same as `dmr serve service restart`). Requires allow_from.
	DmrRestartTrigger string `json:"dmr_restart_trigger"`
	DmrRestartToken   string `json:"dmr_restart_token"`

	// InboundMediaEnabled: when true (default), download user-sent image/file messages via Feishu message-resource API into workspace.
	InboundMediaEnabled bool `json:"inbound_media_enabled"`
	// InboundMediaMaxBytes caps a single downloaded resource (default same as send cap).
	InboundMediaMaxBytes int `json:"inbound_media_max_bytes"`
	// InboundMediaDir is a subdirectory under Workspace (or fallback root) for saved files.
	InboundMediaDir string `json:"inbound_media_dir"`
	// InboundMediaTimeoutSec limits HTTP download time per resource (default 45).
	InboundMediaTimeoutSec int `json:"inbound_media_timeout_sec"`
	// InboundMediaRetentionDays: delete date subfolders (YYYY-MM-DD) older than this many days under inbound dir; 0 disables cleanup.
	InboundMediaRetentionDays int `json:"inbound_media_retention_days"`

	// InboundReplyContextEnabled: when true (default), if the event has parent_id, fetch that message via im/v1 message/get and prepend a quoted block to the RunAgent user text.
	InboundReplyContextEnabled bool `json:"inbound_reply_context_enabled"`
	// InboundReplyContextTimeoutSec limits message/get for the parent message (default 12).
	InboundReplyContextTimeoutSec int `json:"inbound_reply_context_timeout_sec"`
	// InboundReplyContextMaxRunes caps parent message body (after parsing) in the quoted block; 0 means default 8000.
	InboundReplyContextMaxRunes int `json:"inbound_reply_context_max_runes"`
}

func defaultFeishuConfig() FeishuConfig {
	return FeishuConfig{
		ApprovalTimeoutSec:           300,
		DedupTTLMinutes:              10,
		InboundMediaEnabled:          true,
		InboundReplyContextEnabled:   true,
		InboundReplyContextTimeoutSec: defaultInboundReplyContextTimeoutSec,
		InboundReplyContextMaxRunes:   defaultInboundReplyContextMaxRunes,
	}
}

func parseFeishuConfig(jsonStr string) (FeishuConfig, error) {
	cfg := defaultFeishuConfig()
	if jsonStr == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
		return cfg, err
	}
	if cfg.ApprovalTimeoutSec <= 0 {
		cfg.ApprovalTimeoutSec = 300
	}
	if cfg.DedupTTLMinutes <= 0 {
		cfg.DedupTTLMinutes = 10
	}
	if cfg.SendFileMaxBytes <= 0 {
		cfg.SendFileMaxBytes = defaultSendFileMaxBytes
	}
	if cfg.InboundMediaMaxBytes <= 0 {
		cfg.InboundMediaMaxBytes = cfg.SendFileMaxBytes
	}
	if strings.TrimSpace(cfg.InboundMediaDir) == "" {
		cfg.InboundMediaDir = defaultInboundMediaDir
	}
	if cfg.InboundMediaTimeoutSec <= 0 {
		cfg.InboundMediaTimeoutSec = defaultInboundMediaTimeoutSec
	}
	if cfg.InboundMediaRetentionDays < 0 {
		cfg.InboundMediaRetentionDays = 0
	}
	if cfg.InboundReplyContextTimeoutSec <= 0 {
		cfg.InboundReplyContextTimeoutSec = defaultInboundReplyContextTimeoutSec
	}
	if cfg.InboundReplyContextMaxRunes <= 0 {
		cfg.InboundReplyContextMaxRunes = defaultInboundReplyContextMaxRunes
	}
	return cfg, nil
}

func (c FeishuConfig) sendFileMaxBytes() int64 {
	if c.SendFileMaxBytes <= 0 {
		return int64(defaultSendFileMaxBytes)
	}
	return int64(c.SendFileMaxBytes)
}

func (c FeishuConfig) approvalTimeout() time.Duration {
	return time.Duration(c.ApprovalTimeoutSec) * time.Second
}

func (c FeishuConfig) dedupTTL() time.Duration {
	return time.Duration(c.DedupTTLMinutes) * time.Minute
}

func (c FeishuConfig) inboundMediaMaxBytes() int64 {
	if c.InboundMediaMaxBytes <= 0 {
		return int64(defaultSendFileMaxBytes)
	}
	return int64(c.InboundMediaMaxBytes)
}

func (c FeishuConfig) inboundMediaTimeout() time.Duration {
	sec := c.InboundMediaTimeoutSec
	if sec <= 0 {
		sec = defaultInboundMediaTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

func (c FeishuConfig) replyContextTimeout() time.Duration {
	sec := c.InboundReplyContextTimeoutSec
	if sec <= 0 {
		sec = defaultInboundReplyContextTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

func (c FeishuConfig) inboundReplyContextMaxRunes() int {
	if c.InboundReplyContextMaxRunes <= 0 {
		return defaultInboundReplyContextMaxRunes
	}
	return c.InboundReplyContextMaxRunes
}

// inboundStorageRoot returns absolute directory under workspace (or config_base_dir fallback) for inbound media.
func (c FeishuConfig) inboundStorageRoot() (root string, err error) {
	base := strings.TrimSpace(c.Workspace)
	if base == "" {
		base = strings.TrimSpace(c.ConfigBaseDir)
	}
	if base == "" {
		return "", fmt.Errorf("feishu inbound: workspace and config_base_dir are empty (DMR should inject workspace)")
	}
	base, err = filepath.Abs(filepath.Clean(base))
	if err != nil {
		return "", err
	}
	sub := strings.TrimSpace(c.InboundMediaDir)
	if sub == "" {
		sub = defaultInboundMediaDir
	}
	sub = filepath.Clean(sub)
	if sub == "." || strings.Contains(sub, "..") {
		return "", fmt.Errorf("feishu inbound: invalid inbound_media_dir %q", c.InboundMediaDir)
	}
	joined := filepath.Join(base, sub)
	// Clean can make relative; ensure we stay under base
	joined, err = filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(base, joined); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("feishu inbound: inbound_media_dir escapes workspace")
	}
	return joined, nil
}

// resolveExtraPromptPath resolves path for extra_prompt_file: absolute as-is, else join with config_base_dir.
func resolveExtraPromptPath(path, configBaseDir string) string {
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

// buildResolvedExtraPrompt loads file (if set) then appends ExtraPrompt. Order: file body, blank line, inline extra_prompt.
func buildResolvedExtraPrompt(cfg FeishuConfig) (string, error) {
	var parts []string
	if fp := strings.TrimSpace(cfg.ExtraPromptFile); fp != "" {
		abs := resolveExtraPromptPath(fp, cfg.ConfigBaseDir)
		b, err := os.ReadFile(abs)
		if err != nil {
			return "", fmt.Errorf("extra_prompt_file %q: %w", fp, err)
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			parts = append(parts, s)
		}
	}
	if s := strings.TrimSpace(cfg.ExtraPrompt); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n\n"), nil
}
