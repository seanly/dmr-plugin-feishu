package inbound

import (
	"testing"
	"time"
)

func TestDeduper(t *testing.T) {
	d := NewDeduper(100 * time.Millisecond)

	t.Run("first occurrence is not duplicate", func(t *testing.T) {
		if d.IsDuplicate("msg1") {
			t.Error("First occurrence should not be duplicate")
		}
	})

	t.Run("second occurrence is duplicate", func(t *testing.T) {
		if !d.IsDuplicate("msg1") {
			t.Error("Second occurrence should be duplicate")
		}
	})

	t.Run("different message is not duplicate", func(t *testing.T) {
		if d.IsDuplicate("msg2") {
			t.Error("Different message should not be duplicate")
		}
	})

	t.Run("empty string is not duplicate", func(t *testing.T) {
		if d.IsDuplicate("") {
			t.Error("Empty string should not be duplicate")
		}
	})
}

func TestDeduperCleanup(t *testing.T) {
	// Note: Deduper cleanup happens every 100 inserts, so we test with many messages
	d := NewDeduper(50 * time.Millisecond)

	// Add many messages to trigger cleanup (every 100)
	for i := 0; i < 105; i++ {
		d.IsDuplicate(string(rune('a' + i%26)))
	}

	// Deduper should have triggered cleanup at 100th insert
	// but expired entries are only those older than TTL
}
