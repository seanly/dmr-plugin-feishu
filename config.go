package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultSendFileMaxBytes = 30 * 1024 * 1024 // 30 MiB

// FeishuConfig is loaded from the plugin InitRequest.ConfigJSON (YAML becomes map then JSON in DMR).
type FeishuConfig struct {
	// ConfigBaseDir is injected by DMR (absolute directory of the main config file). Used to resolve relative extra_prompt_file paths.
	ConfigBaseDir string `json:"config_base_dir"`
	AppID               string   `json:"app_id"`
	AppSecret           string   `json:"app_secret"`
	VerificationToken   string   `json:"verification_token"`
	EncryptKey          string   `json:"encrypt_key"`
	AllowFrom          []string `json:"allow_from"`
	ApprovalTimeoutSec int      `json:"approval_timeout_sec"`
	DedupTTLMinutes     int      `json:"dedup_ttl_minutes"`
	// SendFileMaxBytes caps feishu.send_file uploads (default 30 MiB).
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
}

func defaultFeishuConfig() FeishuConfig {
	return FeishuConfig{
		ApprovalTimeoutSec: 300,
		DedupTTLMinutes:    10,
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
