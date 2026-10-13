package router

import (
	"sync"
	"time"
)

// Sticky remembers which channel each alert group was last routed to, so
// resolved notifications follow their firing ones. Entries expire after TTL
// so groups that never resolve don't accumulate. It is in-memory only.
type Sticky struct {
	TTL time.Duration
	Now func() time.Time

	mu      sync.Mutex
	entries map[string]stickyEntry
}

type stickyEntry struct {
	channel Channel
	expires time.Time
}

// NewSticky returns an empty Sticky with the given TTL.
func NewSticky(ttl time.Duration) *Sticky {
	return &Sticky{TTL: ttl, entries: map[string]stickyEntry{}}
}

func (s *Sticky) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Set records ch for key and prunes expired entries.
func (s *Sticky) Set(key string, ch Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, e := range s.entries {
		if now.After(e.expires) {
			delete(s.entries, k)
		}
	}
	s.entries[key] = stickyEntry{channel: ch, expires: now.Add(s.TTL)}
}

// Get returns the channel recorded for key, if it hasn't expired.
func (s *Sticky) Get(key string) (Channel, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[key]
	if !ok || s.now().After(e.expires) {
		return "", false
	}
	return e.channel, true
}

// Delete forgets key.
func (s *Sticky) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, key)
}

// Len returns the number of entries, including expired ones not yet pruned.
func (s *Sticky) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
