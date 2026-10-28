package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"food-order/parser/internal/menu"
)

type stub struct {
	res  menu.Result
	err  error
	date time.Time
}

func (s *stub) Get(_ context.Context, d time.Time) (menu.Result, error) {
	s.date = d
	return s.res, s.err
}

func do(t *testing.T, s *stub, url string) *httptest.ResponseRecorder {
	t.Helper()
	loc := time.FixedZone("EET", 2*3600)
	now := func() time.Time { return time.Date(2026, 10, 27, 16, 0, 0, 0, loc) }
	h := New(s, loc, now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec
}

func TestFetchOK(t *testing.T) {
	s := &stub{res: menu.Result{Date: "2026-10-28", Items: []menu.Item{{Name: "  Ciorbă DE pui", Category: "Supe", Price: 30}}}}
	rec := do(t, s, "/v1/fetch")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := s.date.Format("2006-01-02"); got != "2026-10-28" {
		t.Errorf("default date should be tomorrow, got %s", got)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Items) != 1 {
		t.Fatalf("bad body: %v %s", err, rec.Body)
	}
	if out.Items[0]["name_norm"] != "ciorbă de pui" || out.Items[0]["category"] != "Supe" {
		t.Errorf("unexpected item: %v", out.Items[0])
	}
}

func TestFetchErrors(t *testing.T) {
	cases := []struct {
		name  string
		url   string
		err   error
		code  int
		ecode string
	}{
		{"empty", "/v1/fetch?date=2026-10-28", menu.ErrEmpty, 404, "menu_empty"},
		{"throttled", "/v1/fetch", &menu.ThrottledError{RetryAfter: 5 * time.Minute}, 429, "throttled"},
		{"timeout", "/v1/fetch", context.DeadlineExceeded, 504, "timeout"},
		{"other", "/v1/fetch", errors.New("boom"), 502, "fetch_failed"},
		{"bad date", "/v1/fetch?date=28.10.2026", nil, 400, "bad_date"},
	}
	for _, c := range cases {
		rec := do(t, &stub{err: c.err}, c.url)
		var body struct {
			Error struct{ Code string } `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if rec.Code != c.code || body.Error.Code != c.ecode {
			t.Errorf("%s: got %d %q, want %d %q", c.name, rec.Code, body.Error.Code, c.code, c.ecode)
		}
		if c.name == "throttled" && rec.Header().Get("Retry-After") == "" {
			t.Error("throttled answer needs Retry-After")
		}
	}
}

func TestHealthz(t *testing.T) {
	if rec := do(t, &stub{}, "/healthz"); rec.Code != 200 {
		t.Fatalf("healthz: %d", rec.Code)
	}
}
