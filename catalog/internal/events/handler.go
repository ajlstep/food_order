// Package events receives the NATS events meant for catalog, decodes the
// envelope, drops duplicates (processed_events) and calls the service.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"food-order/catalog/internal/service"
	contracts "food-order/shared/contracts/catalog"
	"food-order/shared/contracts/food"
	"food-order/shared/events"
	"food-order/shared/streams"
)

// Handler handles one raw event.
type Handler struct {
	Svc *service.Service
}

// New makes a Handler.
func New(svc *service.Service) *Handler { return &Handler{Svc: svc} }

// Handle processes one event; it is a streams.Handler.
func (h *Handler) Handle(ctx context.Context, raw []byte) error {
	var env events.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: envelope: %v", streams.ErrDrop, err)
	}
	st := h.Svc.Store
	if env.ID != "" {
		done, err := st.Processed(ctx, env.ID)
		if err != nil {
			return err
		}
		if done {
			h.Svc.Log.DebugContext(ctx, "duplicate event skipped", "event_id", env.ID, "type", env.Type)
			return nil // a duplicate
		}
	}

	var err error
	switch env.Type {
	case food.SubjectMenuFetched:
		var d contracts.MenuFetched
		if err = decode(env, &d); err == nil {
			_, err = h.Svc.Import(ctx, d)
		}
	case food.SubjectDishAnalyzed:
		var d contracts.DishAnalyzed
		if err = decode(env, &d); err == nil {
			err = need(d.MenuItemID > 0, "menu_item_id is required")
		}
		if err == nil {
			err = h.Svc.OnAnalyzed(ctx, d)
		}
	case food.SubjectDishEmbedded:
		var d contracts.DishEmbedded
		if err = decode(env, &d); err == nil {
			err = need(d.DishID > 0, "dish_id is required")
		}
		if err == nil {
			err = h.Svc.OnEmbedded(ctx, d)
		}
	default:
		return fmt.Errorf("%w: unknown event type %q", streams.ErrDrop, env.Type)
	}
	if err != nil {
		var se *service.Error
		if errors.As(err, &se) {
			return fmt.Errorf("%w: %s: %v", streams.ErrDrop, env.Type, err)
		}
		return err
	}
	if env.ID != "" {
		return st.MarkProcessed(ctx, env.ID, env.Type, h.Svc.Now())
	}
	return nil
}

func decode(env events.Envelope, out any) error {
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("%w: %s data: %v", streams.ErrDrop, env.Type, err)
	}
	return nil
}

func need(ok bool, msg string) error {
	if !ok {
		return fmt.Errorf("%w: %s", streams.ErrDrop, msg)
	}
	return nil
}
