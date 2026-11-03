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

	"food-order/catalog/internal/config"
	"food-order/catalog/internal/service"
	"food-order/catalog/internal/store"
	contracts "food-order/shared/contracts/catalog"
	"food-order/shared/contracts/food"
	"food-order/shared/events"
	"food-order/shared/streams"
)

type nobody struct{}

func (nobody) Publish(context.Context, string, any) error { return nil }

func TestHandle(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Defaults()
	cfg.AnalysisEnabled = false
	svc := service.New(st, nobody{}, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h := New(svc)
	ctx := context.Background()

	env, _ := events.NewEnvelope(food.SubjectMenuFetched, contracts.MenuFetched{RequestID: "r1", Days: []contracts.MenuDay{
		{Date: "2026-11-02", Items: []contracts.SourceItem{{Name: "Ciorbă"}}},
	}}, time.Now())
	raw, _ := json.Marshal(env)
	for i := 0; i < 2; i++ { // the second time is a duplicate
		if err := h.Handle(ctx, raw); err != nil {
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
		{food.SubjectMenuFetched, contracts.MenuFetched{RequestID: "r2"}}, // no days
		{food.SubjectDishAnalyzed, contracts.DishAnalyzed{}},              // no menu_item_id
		{food.SubjectDishEmbedded, contracts.DishEmbedded{DishID: 1}},     // empty vector
		{"food.something.else", map[string]int{}},                         // unknown type
	}
	for _, b := range bad {
		env, _ := events.NewEnvelope(b.subject, b.data, time.Now())
		raw, _ := json.Marshal(env)
		if err := h.Handle(ctx, raw); !errors.Is(err, streams.ErrDrop) {
			t.Errorf("%s: want a permanent error, got %v", b.subject, err)
		}
	}
	if err := h.Handle(ctx, []byte("{")); !errors.Is(err, streams.ErrDrop) {
		t.Errorf("broken envelope: %v", err)
	}
}
