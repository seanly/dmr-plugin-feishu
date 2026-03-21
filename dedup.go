package main

import (
	"sync"
	"time"
)

type deduper struct {
	mu  sync.Mutex
	m   map[string]time.Time
	ttl time.Duration
}

func newDeduper(ttl time.Duration) *deduper {
	return &deduper{m: make(map[string]time.Time), ttl: ttl}
}

// isDuplicate returns true if id was seen recently; otherwise records id and returns false.
func (d *deduper) isDuplicate(id string) bool {
	if id == "" || d.ttl <= 0 {
		return false
	}
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.m[id]; ok && now.Sub(t) < d.ttl {
		return true
	}
	d.m[id] = now
	if len(d.m) > 5000 {
		for k, t := range d.m {
			if now.Sub(t) > d.ttl {
				delete(d.m, k)
			}
		}
	}
	return false
}
