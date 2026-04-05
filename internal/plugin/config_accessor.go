package plugin

import "time"

// InboundMediaEnabled implements bot.ConfigAccessor.
func (c Config) InboundMediaEnabled() bool { return c.GetInboundMediaEnabled() }

// InboundMediaMaxBytes implements bot.ConfigAccessor.
func (c Config) InboundMediaMaxBytes() int64 { return c.GetInboundMediaMaxBytes() }

// InboundMediaTimeout implements bot.ConfigAccessor.
func (c Config) InboundMediaTimeout() time.Duration { return c.GetInboundMediaTimeout() }

// InboundStorageRoot implements bot.ConfigAccessor.
func (c Config) InboundStorageRoot() (string, error) { return c.GetInboundStorageRoot() }

// InboundMediaRetentionDays implements bot.ConfigAccessor.
func (c Config) InboundMediaRetentionDays() int { return c.GetInboundMediaRetentionDays() }
