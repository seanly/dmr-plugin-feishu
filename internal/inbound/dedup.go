package inbound

import (
	"sync"
	"time"
)

// Deduper provides message deduplication with TTL.
type Deduper struct {
	mu      sync.RWMutex
	entries map[string]time.Time
	ttl     time.Duration
}

// NewDeduper creates a new deduplicator with the given TTL.
func NewDeduper(ttl time.Duration) *Deduper {
	return &Deduper{
		entries: make(map[string]time.Time),
		ttl:     ttl,
	}
}

// IsDuplicate checks if a message ID is a duplicate.
func (d *Deduper) IsDuplicate(msgID string) bool {
	if d == nil || msgID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if _, exists := d.entries[msgID]; exists {
		return true
	}
	d.entries[msgID] = now

	// Cleanup expired entries periodically (simple approach: every 100 inserts)
	if len(d.entries)%100 == 0 {
		for id, ts := range d.entries {
			if now.Sub(ts) > d.ttl {
				delete(d.entries, id)
			}
		}
	}
	return false
}
