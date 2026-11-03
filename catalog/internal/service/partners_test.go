package service

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"food-order/catalog/internal/config"
	"food-order/catalog/internal/store"
	contracts "food-order/shared/contracts/catalog"
)

const partnersJSON = `{"partners": [{
	"key": "bistro", "name": "Bistro", "source": "parser",
	"schedule": [
		{"days": [1, 2, 3, 4], "order_until": "10:00", "delivery_from": "11:30", "delivery_to": "13:30"},
		{"days": [5], "days_before": 1, "order_until": "18:00"}
	],
	"closed": [{"date": "2026-11-05", "reason": "Sărbătoare"}]
}]}`

func newPartners(t *testing.T) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Defaults()
	cfg.AnalysisEnabled, cfg.EmbedEnabled = false, false
	s := New(st, &fakeBus{}, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	path := filepath.Join(t.TempDir(), "partners.json")
	if err := os.WriteFile(path, []byte(partnersJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.LoadPartners(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	// loading the same file again changes nothing (it runs at every start)
	if err := s.LoadPartners(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAvailability(t *testing.T) {
	s := newPartners(t)
	loc := s.Cfg.Location
	at := func(day, h int) int64 { return time.Date(2026, 11, day, h, 0, 0, 0, loc).UnixMilli() }
	cases := []struct {
		name   string
		date   string
		closed bool
		reason string
		until  int64
	}{
		{"Monday, same day", "2026-11-02", false, "", at(2, 10)},
		{"Friday, the day before", "2026-11-06", false, "", at(5, 18)},
		{"a closure beats the schedule", "2026-11-05", true, "Sărbătoare", 0},
		{"a day it does not work", "2026-11-07", true, "nu lucrează în ziua asta", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := s.Partners(context.Background(), c.date)
			if err != nil || len(out.Items) != 1 {
				t.Fatalf("%+v %v", out, err)
			}
			got := out.Items[0]
			if got.Closed != c.closed || got.Reason != c.reason || got.OrderUntilMs != c.until {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestDishesCarryThePartner(t *testing.T) {
	s := newPartners(t)
	ctx := context.Background()
	menu := contracts.MenuFetched{RequestID: "r", Source: "parser", Days: []contracts.MenuDay{
		{Date: "2026-11-02", Items: []contracts.SourceItem{{Name: "Ciorbă"}}},
		{Date: "2026-11-05", Items: []contracts.SourceItem{{Name: "Pilaf"}}},
	}}
	if _, err := s.Import(ctx, menu); err != nil {
		t.Fatal(err)
	}
	open, err := s.Menu(ctx, "2026-11-02")
	if err != nil || len(open.Items) != 1 {
		t.Fatalf("%+v %v", open, err)
	}
	if d := open.Items[0]; d.PartnerName != "Bistro" || d.OrderUntilMs == 0 || d.Closed {
		t.Fatalf("dish: %+v", d)
	}
	closed, err := s.Menu(ctx, "2026-11-05")
	if err != nil || !closed.Items[0].Closed {
		t.Fatalf("%+v %v", closed, err)
	}
}

func TestBadPartnersFile(t *testing.T) {
	cases := map[string]string{
		"bad hour": `{"partners": [{"key": "a", "name": "A", "source": "s", "schedule": [{"days": [1], "order_until": "25:00"}]}]}`,
		"bad day":  `{"partners": [{"key": "a", "name": "A", "source": "s", "schedule": [{"days": [8], "order_until": "10:00"}]}]}`,
		"no name":  `{"partners": [{"key": "a", "source": "s"}]}`,
		"bad date": `{"partners": [{"key": "a", "name": "A", "source": "s", "closed": [{"date": "mâine"}]}]}`,
		"not json": `{`,
	}
	for name, raw := range cases {
		if _, err := parsePartners([]byte(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNoPartnersFile(t *testing.T) {
	s := newPartners(t)
	if err := s.LoadPartners(context.Background(), filepath.Join(t.TempDir(), "nu-exista.json")); err != nil {
		t.Fatal(err)
	}
}
