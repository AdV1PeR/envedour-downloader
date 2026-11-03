package bot

import (
	"fmt"
	"sync"
	"time"
)

// rateLimiter enforces a per-user sliding-window cap on download requests so
// one spammer cannot flood the queue or exhaust the device.
type rateLimiter struct {
	mu      sync.Mutex
	hits    map[int64][]time.Time
	maxKeys int
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{hits: make(map[int64][]time.Time), maxKeys: 4096}
}

// allow reports whether userID may make a request right now. limit <= 0
// disables limiting. When rejected, it returns how long until the next
// request would pass.
func (r *rateLimiter) allow(userID int64, limit int, window time.Duration) (bool, time.Duration) {
	if limit <= 0 {
		return true, 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Cap the map: many distinct users must not grow it without bounds.
	// Evicted users simply start with a fresh window.
	if _, known := r.hits[userID]; !known && len(r.hits) >= r.maxKeys {
		var oldestID int64
		var oldest time.Time
		first := true
		for id, hits := range r.hits {
			if len(hits) == 0 {
				delete(r.hits, id)
				continue
			}
			if first || hits[0].Before(oldest) {
				oldest, oldestID, first = hits[0], id, false
			}
		}
		if len(r.hits) >= r.maxKeys {
			delete(r.hits, oldestID)
		}
	}

	now := time.Now()
	cutoff := now.Add(-window)

	// Drop timestamps outside the window.
	kept := r.hits[userID][:0]
	for _, t := range r.hits[userID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}

	if len(kept) >= limit {
		// The oldest hit in the window determines when a slot frees up.
		wait := kept[0].Add(window).Sub(now)
		if wait < 0 {
			wait = 0
		}
		r.hits[userID] = kept
		return false, wait
	}

	r.hits[userID] = append(kept, now)
	return true, 0
}

// urlDedup remembers recently accepted URLs per user so a link posted
// repeatedly does not enqueue the same download over and over.
type urlDedup struct {
	mu      sync.Mutex
	recent  map[string]time.Time
	window  time.Duration
	maxKeys int
}

func newURLDedup(window time.Duration) *urlDedup {
	return &urlDedup{
		recent:  make(map[string]time.Time),
		window:  window,
		maxKeys: 512,
	}
}

// seen reports whether this user posted the same URL within the window and
// records it otherwise. The map is capped: once it grows beyond maxKeys the
// oldest entry is evicted.
func (d *urlDedup) seen(userID int64, rawURL string) bool {
	key := fmt.Sprintf("%d:%s", userID, rawURL)

	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if t, ok := d.recent[key]; ok && now.Sub(t) < d.window {
		return true
	}
	if len(d.recent) >= d.maxKeys {
		oldestKey := ""
		var oldest time.Time
		first := true
		for k, t := range d.recent {
			if first || t.Before(oldest) {
				oldest, oldestKey, first = t, k, false
			}
		}
		delete(d.recent, oldestKey)
	}
	d.recent[key] = now
	return false
}
