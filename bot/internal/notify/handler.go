// Package notify reads the bot's queue (notify.telegram, stream NOTIFY) and
// hands every message to the Telegram side. It is the only thing the bot
// takes from NATS: everything it asks for goes through the gateway.
package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	contracts "food-order/shared/contracts/core"
	"food-order/shared/contracts/food"
	"food-order/shared/contracts/notify"
	"food-order/shared/streams"
)

// Recipient is who a message is for, as the gateway chose.
type Recipient struct {
	UserID  string
	ChatRef string
}

// Sender shows a message. Implemented by the tg package. An error means
// "try again"; an error wrapping streams.ErrDrop means "give up".
type Sender interface {
	RecommendationReady(ctx context.Context, to Recipient, r contracts.RecommendationReady) error
	RatingRequested(ctx context.Context, to Recipient, r contracts.RatingRequested) error
	OrgInvite(ctx context.Context, to Recipient, d food.OrgInvite) error
	OrgNotice(ctx context.Context, to Recipient, d food.OrgNotice) error
	Alert(ctx context.Context, a food.Alert) error
}

// Handler decodes queue messages and calls the Sender. It remembers the ids
// it handled, so a message delivered twice is not shown twice.
type Handler struct {
	sender Sender
	log    *slog.Logger
	seen   *seenSet
}

// NewHandler makes a Handler that remembers the last 1000 message ids.
func NewHandler(s Sender, log *slog.Logger) *Handler {
	return &Handler{sender: s, log: log, seen: newSeenSet(1000)}
}

// Handle works on one raw message; it is a streams.Handler.
func (h *Handler) Handle(ctx context.Context, raw []byte) error {
	var m notify.Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("%w: the message is not valid: %v", streams.ErrDrop, err)
	}
	if m.ID != "" && h.seen.has(m.ID) {
		h.log.DebugContext(ctx, "duplicate message skipped", "kind", m.Kind)
		return nil
	}
	if err := h.dispatch(ctx, m); err != nil {
		return err
	}
	if m.ID != "" {
		h.seen.add(m.ID)
	}
	return nil
}

func (h *Handler) dispatch(ctx context.Context, m notify.Message) error {
	to := Recipient{UserID: m.UserID, ChatRef: m.ChatRef}
	switch m.Kind {
	case notify.KindRecommendation:
		var d contracts.RecommendationReady
		if err := decode(m, &d); err != nil {
			return err
		}
		return h.sender.RecommendationReady(ctx, to, d)
	case notify.KindRating:
		var d contracts.RatingRequested
		if err := decode(m, &d); err != nil {
			return err
		}
		return h.sender.RatingRequested(ctx, to, d)
	case notify.KindOrgInvite:
		var d food.OrgInvite
		if err := decode(m, &d); err != nil {
			return err
		}
		return h.sender.OrgInvite(ctx, to, d)
	case notify.KindOrgNotice:
		var d food.OrgNotice
		if err := decode(m, &d); err != nil {
			return err
		}
		return h.sender.OrgNotice(ctx, to, d)
	case notify.KindAlert:
		var d food.Alert
		if err := decode(m, &d); err != nil {
			return err
		}
		return h.sender.Alert(ctx, d)
	}
	return fmt.Errorf("%w: unknown kind %q", streams.ErrDrop, m.Kind)
}

func decode(m notify.Message, v any) error {
	if err := json.Unmarshal(m.Data, v); err != nil {
		return fmt.Errorf("%w: %s data: %v", streams.ErrDrop, m.Kind, err)
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
