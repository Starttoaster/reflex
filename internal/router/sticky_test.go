package router

import (
	"testing"
	"time"
)

func TestStickyExpiry(t *testing.T) {
	now := testNow
	s := NewSticky(time.Hour)
	s.Now = func() time.Time { return now }

	s.Set("a", Urgent)
	if ch, ok := s.Get("a"); !ok || ch != Urgent {
		t.Fatalf("Get(a) = %v, %v", ch, ok)
	}

	now = now.Add(2 * time.Hour)
	if _, ok := s.Get("a"); ok {
		t.Error("Get(a) found an expired entry")
	}

	// Setting another key prunes the expired one.
	s.Set("b", Info)
	if n := s.Len(); n != 1 {
		t.Errorf("Len() = %d after pruning, want 1", n)
	}

	s.Delete("b")
	if _, ok := s.Get("b"); ok {
		t.Error("Get(b) found a deleted entry")
	}
}
