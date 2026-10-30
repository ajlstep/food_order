package events

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"food-order/catalog/internal/bus"
	"food-order/catalog/internal/config"
	"food-order/catalog/internal/contracts"
	"food-order/catalog/internal/service"
	"food-order/catalog/internal/store"
)

func TestHandle(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Defaults()
	cfg.AnalysisEnabled = false
	svc := service.New(st, bus.Off{}, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := New(svc)
	ctx := context.Background()

	env, _ := bus.NewEnvelope(contracts.SubjectMenuFetched, contracts.MenuFetched{RequestID: "r1", Days: []contracts.MenuDay{
		{Date: "2026-11-02", Items: []contracts.SourceItem{{Name: "Ciorbă"}}},
	}}, time.Now())
	raw, _ := json.Marshal(env)
	for i := 0; i < 2; i++ { // the second time is a duplicate
		if err := h.Handle(ctx, contracts.SubjectMenuFetched, raw); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := svc.Menu(ctx, "2026-11-02")
	if len(m.Items) != 1 || m.Items[0].Revision != 1 {
		t.Errorf("menu: %+v", m.Items)
	}

	bad := []struct {
		subject string
		data    any
	}{
		{contracts.SubjectMenuFetched, contracts.MenuFetched{RequestID: "r2"}}, // no days
		{contracts.SubjectDishAnalyzed, contracts.DishAnalyzed{}},              // no menu_item_id
		{contracts.SubjectDishEmbedded, contracts.DishEmbedded{DishID: 1}},     // empty vector
		{"food.something.else", map[string]int{}},                              // unknown type
	}
	for _, b := range bad {
		env, _ := bus.NewEnvelope(b.subject, b.data, time.Now())
		raw, _ := json.Marshal(env)
		if err := h.Handle(ctx, b.subject, raw); !errors.Is(err, bus.ErrPermanent) {
			t.Errorf("%s: want a permanent error, got %v", b.subject, err)
		}
	}
	if err := h.Handle(ctx, "x", []byte("{")); !errors.Is(err, bus.ErrPermanent) {
		t.Errorf("broken envelope: %v", err)
	}
}
