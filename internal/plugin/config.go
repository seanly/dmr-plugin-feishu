package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// BotConfig holds configuration for a single Feishu bot instance.
type BotConfig struct {
	AppID             string   `json:"app_id"`
	AppSecret         string   `json:"app_secret"`
	VerificationToken string   `json:"verification_token"`
	EncryptKey        string   `json:"encrypt_key"`
	AllowFrom         []string `json:"allow_from"`
	// GroupEnabled controls whether this bot receives group messages.
	// Default false (only P2P messages are processed).
	GroupEnabled bool `json:"group_enabled"`
	// Approver is the open_id of the admin who receives group chat approvals.
	Approver string `json:"approver"`
}

// Config is loaded from the plugin InitRequest.ConfigJSON.
type Config struct {
	// ConfigBaseDir is injected by DMR (absolute directory of the main config file).
	ConfigBaseDir string `json:"config_base_dir"`
	// Workspace is injected by DMR (same absolute path as fs/shell tools).
	Workspace string `json:"workspace"`

	// Bots holds one or more Feishu bot instances.
	Bots []BotConfig `json:"bots"`

	// Legacy single-bot fields — kept for backward compatibility.
	AppID             string   `json:"app_id"`
	AppSecret         string   `json:"app_secret"`
	VerificationToken string   `json:"verification_token"`
	EncryptKey        string   `json:"encrypt_key"`
	AllowFrom         []string `json:"allow_from"`

	ApprovalTimeoutSec int `json:"approval_timeout_sec"`
	DedupTTLMinutes    int `json:"dedup_ttl_minutes"`

	// SendFileMaxBytesVal caps feishuSendFile uploads (default 30 MiB).
	SendFileMaxBytesVal int `json:"-"`
	SendFileMaxBytesRaw any `json:"send_file_max_bytes"`
	// SendFileRoot if set, path arguments must resolve under this directory.
	SendFileRoot string `json:"send_file_root"`

	// ExtraPrompt is appended after ExtraPromptFile content.
	ExtraPrompt string `json:"extra_prompt"`
	// ExtraPromptFile is UTF-8 text; relative paths resolve against ConfigBaseDir.
	ExtraPromptFile string `json:"extra_prompt_file"`

	// InboundMediaEnabledVal controls inbound media download.
	InboundMediaEnabledVal bool `json:"inbound_media_enabled"`
	// InboundMediaMaxBytesVal caps a single downloaded resource.
	InboundMediaMaxBytesVal int `json:"-"`
	InboundMediaMaxBytesRaw any `json:"inbound_media_max_bytes"`
	// InboundMediaDir is a subdirectory under Workspace for saved files.
	InboundMediaDir string `json:"inbound_media_dir"`
	// InboundMediaTimeoutSec limits HTTP download time per resource.
	InboundMediaTimeoutSec int `json:"inbound_media_timeout_sec"`
	// InboundMediaRetentionDaysVal controls retention cleanup.
	InboundMediaRetentionDaysVal int `json:"inbound_media_retention_days"`

	// InboundReplyContextEnabled: when true (default), fetch parent message for context.
	InboundReplyContextEnabled bool `json:"inbound_reply_context_enabled"`
	// InboundReplyContextTimeoutSec limits message/get for the parent message.
	InboundReplyContextTimeoutSec int `json:"inbound_reply_context_timeout_sec"`
	// InboundReplyContextMaxRunesVal caps parent message body.
	InboundReplyContextMaxRunesVal int `json:"inbound_reply_context_max_runes"`
}

// DefaultConfig returns default configuration.
func DefaultConfig() Config {
	return Config{
		ApprovalTimeoutSec:             300,
		DedupTTLMinutes:                10,
		InboundMediaEnabledVal:         true,
		InboundReplyContextEnabled:     true,
		InboundReplyContextTimeoutSec:  defaultInboundReplyContextTimeoutSec,
		InboundReplyContextMaxRunesVal: defaultInboundReplyContextMaxRunes,
	}
}

// ParseConfig parses JSON configuration string.
func ParseConfig(jsonStr string) (Config, error) {
	cfg := DefaultConfig()
	if jsonStr == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(jsonStr), &cfg); err != nil {
		return cfg, err
	}

	// Backward compat: if no bots[] but legacy app_id is set, convert to single bot.
	if len(cfg.Bots) == 0 && strings.TrimSpace(cfg.AppID) != "" {
		cfg.Bots = []BotConfig{{
			AppID:             cfg.AppID,
			AppSecret:         cfg.AppSecret,
			VerificationToken: cfg.VerificationToken,
			EncryptKey:        cfg.EncryptKey,
			AllowFrom:         cfg.AllowFrom,
		}}
	}

	if cfg.ApprovalTimeoutSec <= 0 {
		cfg.ApprovalTimeoutSec = 300
	}
	if cfg.DedupTTLMinutes <= 0 {
		cfg.DedupTTLMinutes = 10
	}

	// Parse SendFileMaxBytes
	if cfg.SendFileMaxBytesRaw != nil {
		cfg.SendFileMaxBytesVal = parseSizeValue(cfg.SendFileMaxBytesRaw)
	}
	if cfg.SendFileMaxBytesVal <= 0 {
		cfg.SendFileMaxBytesVal = defaultSendFileMaxBytes
	}

	// Parse InboundMediaMaxBytes
	if cfg.InboundMediaMaxBytesRaw != nil {
		cfg.InboundMediaMaxBytesVal = parseSizeValue(cfg.InboundMediaMaxBytesRaw)
	}
	if cfg.InboundMediaMaxBytesVal <= 0 {
		cfg.InboundMediaMaxBytesVal = cfg.SendFileMaxBytesVal
	}
	if strings.TrimSpace(cfg.InboundMediaDir) == "" {
		cfg.InboundMediaDir = defaultInboundMediaDir
	}
	if cfg.InboundMediaTimeoutSec <= 0 {
		cfg.InboundMediaTimeoutSec = defaultInboundMediaTimeoutSec
	}
	if cfg.InboundMediaRetentionDaysVal < 0 {
		cfg.InboundMediaRetentionDaysVal = 0
	}
	if cfg.InboundReplyContextTimeoutSec <= 0 {
		cfg.InboundReplyContextTimeoutSec = defaultInboundReplyContextTimeoutSec
	}
	if cfg.InboundReplyContextMaxRunesVal <= 0 {
		cfg.InboundReplyContextMaxRunesVal = defaultInboundReplyContextMaxRunes
	}
	return cfg, nil
}

func parseSizeValue(raw any) int {
	var sizeStr string
	switch v := raw.(type) {
	case string:
		sizeStr = v
	case float64:
		return int(v)
	case int:
		return v
	default:
		sizeStr = fmt.Sprint(v)
	}
	if sizeStr == "" {
		return 0
	}
	if size, err := ParseSize(sizeStr); err == nil {
		return int(size)
	}
	return 0
}

// ParseSize parses human-readable size strings like "200MB", "50M", "1GB", "100B" to bytes.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}

	// Try parsing as plain number first
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}

	// Parse with unit suffix
	s = strings.ToUpper(s)
	var multiplier int64 = 1

	if strings.HasSuffix(s, "KB") || strings.HasSuffix(s, "K") {
		multiplier = 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "KB"), "K")
	} else if strings.HasSuffix(s, "MB") || strings.HasSuffix(s, "M") {
		multiplier = 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "MB"), "M")
	} else if strings.HasSuffix(s, "GB") || strings.HasSuffix(s, "G") {
		multiplier = 1024 * 1024 * 1024
		s = strings.TrimSuffix(strings.TrimSuffix(s, "GB"), "G")
	} else if strings.HasSuffix(s, "B") {
		// Just bytes, multiplier remains 1
		s = strings.TrimSuffix(s, "B")
	}

	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size format: %s", s)
	}

	return int64(n * float64(multiplier)), nil
}

// GetSendFileMaxBytes returns the max file size in bytes.
func (c Config) GetSendFileMaxBytes() int64 {
	if c.SendFileMaxBytesVal <= 0 {
		return int64(defaultSendFileMaxBytes)
	}
	return int64(c.SendFileMaxBytesVal)
}

// GetApprovalTimeout returns the approval timeout duration.
func (c Config) GetApprovalTimeout() time.Duration {
	return time.Duration(c.ApprovalTimeoutSec) * time.Second
}

// GetDedupTTL returns the deduplication TTL duration.
func (c Config) GetDedupTTL() time.Duration {
	return time.Duration(c.DedupTTLMinutes) * time.Minute
}

// GetInboundMediaEnabled returns whether inbound media is enabled.
func (c Config) GetInboundMediaEnabled() bool { return c.InboundMediaEnabledVal }

// GetInboundMediaMaxBytes returns the max media download size.
func (c Config) GetInboundMediaMaxBytes() int64 {
	if c.InboundMediaMaxBytesVal <= 0 {
		return int64(defaultSendFileMaxBytes)
	}
	return int64(c.InboundMediaMaxBytesVal)
}

// GetInboundMediaTimeout returns the media download timeout.
func (c Config) GetInboundMediaTimeout() time.Duration {
	sec := c.InboundMediaTimeoutSec
	if sec <= 0 {
		sec = defaultInboundMediaTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// GetReplyContextTimeout returns the reply context fetch timeout.
func (c Config) GetReplyContextTimeout() time.Duration {
	sec := c.InboundReplyContextTimeoutSec
	if sec <= 0 {
		sec = defaultInboundReplyContextTimeoutSec
	}
	return time.Duration(sec) * time.Second
}

// GetInboundReplyContextMaxRunes returns the max runes for reply context.
func (c Config) GetInboundReplyContextMaxRunes() int {
	if c.InboundReplyContextMaxRunesVal <= 0 {
		return defaultInboundReplyContextMaxRunes
	}
	return c.InboundReplyContextMaxRunesVal
}

// GetInboundStorageRoot returns absolute directory under workspace for inbound media.
func (c Config) GetInboundStorageRoot() (root string, err error) {
	base := strings.TrimSpace(c.Workspace)
	if base == "" {
		base = strings.TrimSpace(c.ConfigBaseDir)
	}
	if base == "" {
		return "", fmt.Errorf("feishu inbound: workspace and config_base_dir are empty")
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
	joined, err = filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(base, joined); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("feishu inbound: inbound_media_dir escapes workspace")
	}
	return joined, nil
}

// GetInboundMediaRetentionDays returns the retention days.
func (c Config) GetInboundMediaRetentionDays() int { return c.InboundMediaRetentionDaysVal }

// ResolveExtraPromptPath resolves path for extra_prompt_file.
func ResolveExtraPromptPath(path, configBaseDir string) string {
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

// BuildResolvedExtraPrompt loads file (if set) then appends ExtraPrompt.
func BuildResolvedExtraPrompt(cfg Config) (string, error) {
	var parts []string
	if fp := strings.TrimSpace(cfg.ExtraPromptFile); fp != "" {
		abs := ResolveExtraPromptPath(fp, cfg.ConfigBaseDir)
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
