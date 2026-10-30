package menu

import (
	"context"
	"sync"
	"time"
)

// Result is what the service returns for one day.
type Result struct {
	Date      string
	Items     []Item
	FetchedAt time.Time
	Cached    bool
}

// Service wraps a Fetcher: a successful menu is cached for ttl, and the site
// is not asked more often than once per minInterval for the same day
// (this also covers empty or failed answers). Requests are serialised, so the
// site never sees two parallel requests from us.
type Service struct {
	fetcher     Fetcher
	ttl         time.Duration
	minInterval time.Duration
	now         func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	items       []Item
	fetchedAt   time.Time
	lastAttempt time.Time
	lastErr     error // outcome of the last attempt (nil = it worked)
}

func NewService(f Fetcher, ttl, minInterval time.Duration) *Service {
	return &Service{fetcher: f, ttl: ttl, minInterval: minInterval, now: time.Now, entries: map[string]*entry{}}
}

// Get returns the menu for date. Errors: ErrEmpty, *ThrottledError, or the
// fetcher's own error.
func (s *Service) Get(ctx context.Context, date time.Time) (Result, error) {
	key := date.Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	for k, e := range s.entries { // forget old days
		if now.Sub(e.lastAttempt) > 48*time.Hour {
			delete(s.entries, k)
		}
	}

	e := s.entries[key]
	if e != nil && e.items != nil && now.Sub(e.fetchedAt) < s.ttl {
		return Result{Date: key, Items: e.items, FetchedAt: e.fetchedAt, Cached: true}, nil
	}
	if e != nil && now.Sub(e.lastAttempt) < s.minInterval {
		return Result{}, &ThrottledError{RetryAfter: s.minInterval - now.Sub(e.lastAttempt), Last: e.lastErr}
	}
	if e == nil {
		e = &entry{}
		s.entries[key] = e
	}
	e.lastAttempt = now

	items, err := s.fetcher.Fetch(ctx, date)
	if err == nil && len(items) == 0 {
		err = ErrEmpty
	}
	e.lastErr = err
	if err != nil {
		return Result{}, err
	}
	e.items, e.fetchedAt = items, now
	return Result{Date: key, Items: items, FetchedAt: now}, nil
}
