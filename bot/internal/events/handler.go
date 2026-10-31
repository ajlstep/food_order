// Package events receives the NATS events meant for the bot and turns them
// into calls on a Notifier (the Telegram side). The bot publishes nothing.
package events

import (
	"log/slog"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"food-order/bot/internal/contracts"
)

// Notifier is what the bot does with an event. Implemented by the tg package.
type Notifier interface {
	RecommendationReady(ctx context.Context, r contracts.RecommendationReady) error
	RatingRequested(ctx context.Context, r contracts.RatingRequested) error
	Alert(ctx context.Context, a contracts.Alert) error
}

// ErrPermanent marks an event that will never succeed (bad JSON, unknown
// type): it must be dropped, not redelivered.
var ErrPermanent = errors.New("permanent")

// Handler decodes an event envelope and calls the Notifier.
// It remembers the ids it handled, so a redelivered event is not sent twice.
type Handler struct {
	N    Notifier
	seen *seenSet
}

// NewHandler makes a Handler that remembers the last 1000 event ids.
func NewHandler(n Notifier) *Handler {
	return &Handler{N: n, seen: newSeenSet(1000)}
}

// Handle processes one raw event. A nil error means "ack".
func (h *Handler) Handle(ctx context.Context, raw []byte) error {
	var env contracts.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: envelope: %v", ErrPermanent, err)
	}
	if env.ID != "" && h.seen.has(env.ID) {
		slog.Default().DebugContext(ctx, "duplicate event skipped", "event_id", env.ID, "type", env.Type)
		return nil // duplicate
	}

	var err error
	switch env.Type {
	case contracts.SubjectRecommendationReady:
		var d contracts.RecommendationReady
		if err = decode(env, &d); err == nil {
			err = h.N.RecommendationReady(ctx, d)
		}
	case contracts.SubjectRatingRequested:
		var d contracts.RatingRequested
		if err = decode(env, &d); err == nil {
			err = h.N.RatingRequested(ctx, d)
		}
	case contracts.SubjectAlert:
		var d contracts.Alert
		if err = decode(env, &d); err == nil {
			err = h.N.Alert(ctx, d)
		}
	default:
		return fmt.Errorf("%w: unknown event type %q", ErrPermanent, env.Type)
	}
	if err != nil {
		return err
	}
	if env.ID != "" {
		h.seen.add(env.ID)
	}
	return nil
}

func decode(env contracts.Envelope, v any) error {
	if env.Version > 1 {
		return fmt.Errorf("%w: %s version %d is not supported", ErrPermanent, env.Type, env.Version)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		return fmt.Errorf("%w: %s data: %v", ErrPermanent, env.Type, err)
	}
	return nil
}

// seenSet keeps the last n ids (oldest are forgotten first).
type seenSet struct {
	mu    sync.Mutex
	n     int
	ids   map[string]struct{}
	order []string
}

func newSeenSet(n int) *seenSet { return &seenSet{n: n, ids: make(map[string]struct{}, n)} }

func (s *seenSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.ids[id]
	return ok
}

func (s *seenSet) add(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > s.n {
		delete(s.ids, s.order[0])
		s.order = s.order[1:]
	}
}
