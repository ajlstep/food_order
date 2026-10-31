package menu

import (
	"context"
	"log/slog"
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

	// Log gets the cache decisions (debug) and every real request to the site.
	Log *slog.Logger
}

type entry struct {
	items       []Item
	fetchedAt   time.Time
	lastAttempt time.Time
	lastErr     error // outcome of the last attempt (nil = it worked)
}

func NewService(f Fetcher, ttl, minInterval time.Duration) *Service {
	return &Service{fetcher: f, ttl: ttl, minInterval: minInterval, now: time.Now, entries: map[string]*entry{},
		Log: slog.New(slog.DiscardHandler)}
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
		s.Log.DebugContext(ctx, "menu from the cache", "date", key, "dishes", len(e.items), "age", now.Sub(e.fetchedAt).Round(time.Second))
		return Result{Date: key, Items: e.items, FetchedAt: e.fetchedAt, Cached: true}, nil
	}
	if e != nil && now.Sub(e.lastAttempt) < s.minInterval {
		wait := s.minInterval - now.Sub(e.lastAttempt)
		s.Log.InfoContext(ctx, "the site was asked for this day not long ago, not asking again", "date", key,
			"retry_after", wait.Round(time.Second), "last_err", e.lastErr)
		return Result{}, &ThrottledError{RetryAfter: wait, Last: e.lastErr}
	}
	if e == nil {
		e = &entry{}
		s.entries[key] = e
	}
	e.lastAttempt = now

	s.Log.InfoContext(ctx, "asking the site", "date", key)
	items, err := s.fetcher.Fetch(ctx, date)
	if err == nil && len(items) == 0 {
		err = ErrEmpty
	}
	e.lastErr = err
	if err != nil {
		s.Log.WarnContext(ctx, "the site gave no menu", "date", key, "err", err, "ms", float64(s.now().Sub(now).Milliseconds()))
		return Result{}, err
	}
	s.Log.InfoContext(ctx, "menu read from the site", "date", key, "dishes", len(items), "ms", float64(s.now().Sub(now).Milliseconds()))
	for _, it := range items {
		s.Log.DebugContext(ctx, "dish", "date", key, "name", it.Name, "category", it.Category, "price", it.Price,
			"weight", it.Weight, "components", it.Components)
	}
	e.items, e.fetchedAt = items, now
	return Result{Date: key, Items: items, FetchedAt: now}, nil
}
