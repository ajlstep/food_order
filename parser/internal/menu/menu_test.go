package menu

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNormName(t *testing.T) {
	cases := map[string]string{
		"  Ciorbă   de\u00a0PUI ": "ciorbă de pui",
		"Mămăligă cu brânză":      "mămăligă cu brânză",
		"Şnitel":                  "șnitel", // cedilla -> comma-below
		"Борщ  Домашний":          "борщ домашний",
	}
	for in, want := range cases {
		if got := NormName(in); got != want {
			t.Errorf("NormName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePrice(t *testing.T) {
	cases := map[string]float64{"45 lei": 45, "45,50 MDL": 45.5, "Preț: 120.00": 120, "1 pc / 8": 1}
	for in, want := range cases {
		got, ok := ParsePrice(in)
		if !ok || got != want {
			t.Errorf("ParsePrice(%q) = %v,%v want %v", in, got, ok, want)
		}
	}
	if _, ok := ParsePrice("gratis"); ok {
		t.Error("expected no price")
	}
}

type fakeFetcher struct {
	calls int
	items []Item
	err   error
}

func (f *fakeFetcher) Fetch(context.Context, time.Time) ([]Item, error) {
	f.calls++
	return f.items, f.err
}

var day = time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

func TestServiceCachesSuccess(t *testing.T) {
	f := &fakeFetcher{items: []Item{{Name: "Ciorbă", Price: 30}}}
	s := NewService(f, 3*time.Hour, 10*time.Minute)
	clock := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	r, err := s.Get(context.Background(), day)
	if err != nil || r.Cached || len(r.Items) != 1 {
		t.Fatalf("first call: %+v %v", r, err)
	}
	r, err = s.Get(context.Background(), day)
	if err != nil || !r.Cached || f.calls != 1 {
		t.Fatalf("second call should be cached: %+v %v calls=%d", r, err, f.calls)
	}
	clock = clock.Add(4 * time.Hour)
	if _, err = s.Get(context.Background(), day); err != nil || f.calls != 2 {
		t.Fatalf("after ttl the site is asked again: %v calls=%d", err, f.calls)
	}
}

func TestServiceEmptyIsReportedAndThrottled(t *testing.T) {
	f := &fakeFetcher{}
	s := NewService(f, 3*time.Hour, 10*time.Minute)
	clock := time.Date(2026, 10, 27, 16, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	if _, err := s.Get(context.Background(), day); !errors.Is(err, ErrEmpty) {
		t.Fatalf("want ErrEmpty, got %v", err)
	}
	var th *ThrottledError
	if _, err := s.Get(context.Background(), day); !errors.As(err, &th) || f.calls != 1 {
		t.Fatalf("want ThrottledError without a new request, got %v calls=%d", err, f.calls)
	}
	if !errors.Is(th, ErrEmpty) {
		t.Errorf("a throttled answer must carry the last outcome: %v", th)
	}
	clock = clock.Add(11 * time.Minute)
	if _, err := s.Get(context.Background(), day); !errors.Is(err, ErrEmpty) || f.calls != 2 {
		t.Fatalf("after the interval the site is asked again: %v calls=%d", err, f.calls)
	}
}

func TestServiceFetchErrorPassesThrough(t *testing.T) {
	boom := errors.New("boom")
	s := NewService(&fakeFetcher{err: boom}, time.Hour, time.Minute)
	if _, err := s.Get(context.Background(), day); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	// asked again at once: throttled, but it still says what went wrong
	var th *ThrottledError
	if _, err := s.Get(context.Background(), day); !errors.As(err, &th) || !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
}
