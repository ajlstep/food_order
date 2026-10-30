// Package events receives the NATS events meant for core, decodes the
// envelope, drops duplicates (processed_events) and calls the service.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"food-order/core/internal/bus"
	"food-order/core/internal/contracts"
	"food-order/core/internal/service"
	"food-order/core/internal/store"
)

// Handler handles one raw event.
type Handler struct {
	Svc   *service.Service
	Store *store.Store
	Now   func() time.Time
}

// New makes a Handler.
func New(svc *service.Service) *Handler {
	return &Handler{Svc: svc, Store: svc.Store, Now: svc.Now}
}

// Handle processes one event. nil = ack; bus.ErrPermanent = drop; other
// errors = redeliver later.
func (h *Handler) Handle(ctx context.Context, subject string, raw []byte) error {
	var env contracts.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: envelope: %v", bus.ErrPermanent, err)
	}
	if env.Type == "" {
		env.Type = subject
	}
	if env.ID != "" {
		done, err := h.Store.Processed(ctx, env.ID)
		if err != nil {
			return err
		}
		if done {
			return nil // a duplicate
		}
	}

	var err error
	switch env.Type {
	case contracts.SubjectMenuReady:
		var d contracts.MenuReady
		if err = decode(env, &d); err == nil {
			err = need(d.RequestID != "" && d.From != "", "request_id and from are required")
		}
		if err == nil {
			err = h.Svc.OnMenuReady(ctx, d)
		}
	case contracts.SubjectMenuError:
		var d contracts.MenuError
		if err = decode(env, &d); err == nil {
			err = need(d.RequestID != "", "request_id is required")
		}
		if err == nil {
			err = h.Svc.OnMenuError(ctx, d)
		}
	case contracts.SubjectRecommendationGenerated:
		var d contracts.RecommendationGenerated
		if err = decode(env, &d); err == nil {
			err = need(d.RequestID != "", "request_id is required")
		}
		if err == nil {
			err = h.Svc.OnRecommendationGenerated(ctx, d)
		}
	case contracts.SubjectFeedbackParsed:
		var d contracts.FeedbackParsed
		if err = decode(env, &d); err == nil {
			err = need(d.OrderID > 0, "order_id is required")
		}
		if err == nil {
			err = h.Svc.OnFeedbackParsed(ctx, d)
		}
	default:
		return fmt.Errorf("%w: unknown event type %q", bus.ErrPermanent, env.Type)
	}
	if err != nil {
		if _, bad := err.(*service.Error); bad {
			return fmt.Errorf("%w: %s: %v", bus.ErrPermanent, env.Type, err)
		}
		return err
	}
	if env.ID != "" {
		return h.Store.MarkProcessed(ctx, env.ID, env.Type, h.Now())
	}
	return nil
}

func decode(env contracts.Envelope, out any) error {
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("%w: %s data: %v", bus.ErrPermanent, env.Type, err)
	}
	return nil
}

func need(ok bool, msg string) error {
	if !ok {
		return fmt.Errorf("%w: %s", bus.ErrPermanent, msg)
	}
	return nil
}
