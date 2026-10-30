package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"food-order/parser/internal/bus"
	"food-order/parser/internal/contracts"
	"food-order/parser/internal/menu"
)

// fakeMenu answers per date.
type fakeMenu struct {
	byDate map[string]error
	items  []menu.Item
	calls  int
}

func (f *fakeMenu) Get(_ context.Context, d time.Time) (menu.Result, error) {
	f.calls++
	key := d.Format("2006-01-02")
	if err := f.byDate[key]; err != nil {
		return menu.Result{}, err
	}
	return menu.Result{Date: key, Items: f.items}, nil
}

type published struct {
	subject string
	data    json.RawMessage
}

type fakePub struct {
	out  []published
	fail bool
}

func (p *fakePub) Publish(_ context.Context, subject string, data any) error {
	if p.fail {
		return errors.New("broker down")
	}
	raw, _ := json.Marshal(data)
	p.out = append(p.out, published{subject, raw})
	return nil
}

func newHandler(m *fakeMenu) (*Handler, *fakePub) {
	loc, _ := time.LoadLocation("Europe/Chisinau")
	p := &fakePub{}
	return New(m, p, loc, slog.New(slog.NewTextHandler(io.Discard, nil))), p
}

func request(t *testing.T, id string, req contracts.MenuRequested) []byte {
	t.Helper()
	data, _ := json.Marshal(req)
	raw, _ := json.Marshal(contracts.Envelope{ID: id, Type: contracts.SubjectMenuRequested, Version: 1, TS: time.Now(), Data: data})
	return raw
}

func TestFetched(t *testing.T) {
	m := &fakeMenu{items: []menu.Item{
		{Name: "LUNCH 1 Stand", Category: "Lunch Box", Price: 63, Weight: "250/50", Components: []string{"Orez cu legume", "Pui"}},
		{Name: "Pâine", Price: 0},
	}}
	h, p := newHandler(m)
	if err := h.Handle(context.Background(), contracts.SubjectMenuRequested, request(t, "e1", contracts.MenuRequested{RequestID: "r1", From: "2026-11-02"})); err != nil {
		t.Fatal(err)
	}
	if len(p.out) != 1 || p.out[0].subject != contracts.SubjectMenuFetched {
		t.Fatalf("published: %+v", p.out)
	}
	var f contracts.MenuFetched
	_ = json.Unmarshal(p.out[0].data, &f)
	if f.RequestID != "r1" || f.Source != "parser" || len(f.Days) != 1 || f.Days[0].Date != "2026-11-02" || len(f.Days[0].Items) != 2 {
		t.Fatalf("fetched: %+v", f)
	}
	it := f.Days[0].Items[0]
	if it.Name != "LUNCH 1 Stand" || it.Price == nil || *it.Price != 63 || it.WeightText != "250/50" || len(it.Components) != 2 {
		t.Errorf("item: %+v", it)
	}
	if f.Days[0].Items[1].Price != nil {
		t.Error("price 0 means no price")
	}

	// the same envelope again: no second answer, no second visit to the site
	if err := h.Handle(context.Background(), contracts.SubjectMenuRequested, request(t, "e1", contracts.MenuRequested{RequestID: "r1", From: "2026-11-02"})); err != nil {
		t.Fatal(err)
	}
	if len(p.out) != 1 || m.calls != 1 {
		t.Errorf("duplicate handled again: %d answers, %d calls", len(p.out), m.calls)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name string
		req  contracts.MenuRequested
		err  error
		code string
	}{
		{"no menu", contracts.MenuRequested{RequestID: "r", From: "2026-11-02"}, menu.ErrEmpty, contracts.ErrNoMenu},
		{"throttled after empty", contracts.MenuRequested{RequestID: "r", From: "2026-11-02"},
			&menu.ThrottledError{RetryAfter: time.Minute, Last: menu.ErrEmpty}, contracts.ErrNoMenu},
		{"site down", contracts.MenuRequested{RequestID: "r", From: "2026-11-02"},
			fmt.Errorf("%w: request: timeout", menu.ErrSourceUnavailable), contracts.ErrSourceUnavailable},
		{"throttled after site down", contracts.MenuRequested{RequestID: "r", From: "2026-11-02"},
			&menu.ThrottledError{RetryAfter: time.Minute, Last: fmt.Errorf("%w: x", menu.ErrSourceUnavailable)}, contracts.ErrSourceUnavailable},
		{"unknown error", contracts.MenuRequested{RequestID: "r", From: "2026-11-02"}, errors.New("boom"), contracts.ErrSourceUnavailable},
		{"site changed", contracts.MenuRequested{RequestID: "r", From: "2026-11-02"},
			fmt.Errorf("%w: no dishes in cards", menu.ErrParseFailed), contracts.ErrParseFailed},
		{"bad from", contracts.MenuRequested{RequestID: "r", From: "mâine"}, nil, contracts.ErrBadRequest},
		{"to before from", contracts.MenuRequested{RequestID: "r", From: "2026-11-05", To: "2026-11-02"}, nil, contracts.ErrBadRequest},
		{"too long", contracts.MenuRequested{RequestID: "r", From: "2026-11-01", To: "2026-12-31"}, nil, contracts.ErrBadRequest},
	}
	for i, c := range cases {
		h, p := newHandler(&fakeMenu{byDate: map[string]error{"2026-11-02": c.err}})
		if err := h.Handle(context.Background(), "", request(t, fmt.Sprint(i), c.req)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(p.out) != 1 || p.out[0].subject != contracts.SubjectMenuError {
			t.Fatalf("%s: published %+v", c.name, p.out)
		}
		var e contracts.MenuError
		_ = json.Unmarshal(p.out[0].data, &e)
		if e.Code != c.code || e.RequestID != "r" || e.Message == "" {
			t.Errorf("%s: %+v, want code %s", c.name, e, c.code)
		}
	}
}

func TestPeriodWithAnEmptyDay(t *testing.T) {
	h, p := newHandler(&fakeMenu{items: []menu.Item{{Name: "Ciorbă"}}, byDate: map[string]error{"2026-11-01": menu.ErrEmpty}})
	if err := h.Handle(context.Background(), "", request(t, "e", contracts.MenuRequested{RequestID: "r", From: "2026-10-31", To: "2026-11-02"})); err != nil {
		t.Fatal(err)
	}
	var f contracts.MenuFetched
	_ = json.Unmarshal(p.out[0].data, &f)
	if p.out[0].subject != contracts.SubjectMenuFetched || len(f.Days) != 2 || f.Days[0].Date != "2026-10-31" || f.Days[1].Date != "2026-11-02" {
		t.Errorf("days with a menu go, the empty one is left out: %s %+v", p.out[0].subject, f)
	}
}

func TestPublishFailsThenRedelivered(t *testing.T) {
	m := &fakeMenu{items: []menu.Item{{Name: "Ciorbă"}}}
	h, p := newHandler(m)
	p.fail = true
	raw := request(t, "e", contracts.MenuRequested{RequestID: "r", From: "2026-11-02"})
	if err := h.Handle(context.Background(), "", raw); err == nil || errors.Is(err, bus.ErrPermanent) {
		t.Fatalf("a failed publish must be redelivered: %v", err)
	}
	p.fail = false
	if err := h.Handle(context.Background(), "", raw); err != nil || len(p.out) != 1 {
		t.Errorf("second delivery: %v %+v", err, p.out)
	}
}

func TestDropped(t *testing.T) {
	h, p := newHandler(&fakeMenu{})
	for name, raw := range map[string][]byte{
		"broken envelope": []byte("{"),
		"no request id":   request(t, "x", contracts.MenuRequested{From: "2026-11-02"}),
	} {
		if err := h.Handle(context.Background(), "", raw); !errors.Is(err, bus.ErrPermanent) {
			t.Errorf("%s: %v", name, err)
		}
	}
	other, _ := json.Marshal(contracts.Envelope{ID: "o", Type: "food.other", Data: json.RawMessage(`{}`)})
	if err := h.Handle(context.Background(), "food.other", other); !errors.Is(err, bus.ErrPermanent) {
		t.Errorf("unknown type: %v", err)
	}
	if len(p.out) != 0 {
		t.Errorf("nothing should be published: %+v", p.out)
	}
}
