// Package events is the parser on NATS: it answers food.menu.requested
// with food.menu.fetched (to catalog) or food.menu.error (to core).
//
// The parser has no database, so duplicates are dropped in memory; a
// duplicate that slips through (after a restart) only sends the same menu
// again, which catalog ignores. The site itself is protected by
// menu.Service: a menu that was found is cached, and the same day is not
// asked again before PARSER_MIN_INTERVAL (the last answer is repeated).
// Retries are core's job, not the parser's.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"food-order/parser/internal/applog"
	"food-order/parser/internal/bus"
	"food-order/parser/internal/contracts"
	"food-order/parser/internal/menu"
)

// MaxDays is the longest period one request may ask for.
const MaxDays = 14

// Getter is what the handler needs from menu.Service.
type Getter interface {
	Get(ctx context.Context, date time.Time) (menu.Result, error)
}

// Handler answers menu requests.
type Handler struct {
	Menu Getter
	Pub  bus.Publisher
	Loc  *time.Location // the dates of a request are days in this zone
	Log  *slog.Logger
	Now  func() time.Time

	mu   sync.Mutex
	seen map[string]time.Time // envelope id -> when it was handled
}

// New makes a Handler.
func New(g Getter, pub bus.Publisher, loc *time.Location, log *slog.Logger) *Handler {
	return &Handler{Menu: g, Pub: pub, Loc: loc, Log: log, Now: time.Now, seen: map[string]time.Time{}}
}

// Handle processes one event. nil = ack; bus.ErrPermanent = drop; other
// errors = redeliver later (only when the answer could not be published).
func (h *Handler) Handle(ctx context.Context, subject string, raw []byte) error {
	var env contracts.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("%w: envelope: %v", bus.ErrPermanent, err)
	}
	if env.Type == "" {
		env.Type = subject
	}
	if env.Type != contracts.SubjectMenuRequested {
		return fmt.Errorf("%w: unknown event type %q", bus.ErrPermanent, env.Type)
	}
	if h.duplicate(env.ID) {
		h.Log.DebugContext(ctx, "duplicate event skipped", "event_id", env.ID)
		return nil
	}
	var req contracts.MenuRequested
	if err := json.Unmarshal(env.Data, &req); err != nil {
		return fmt.Errorf("%w: %s data: %v", bus.ErrPermanent, env.Type, err)
	}
	if req.RequestID == "" { // nobody to answer to
		return fmt.Errorf("%w: request_id is required", bus.ErrPermanent)
	}

	answer, subjectOut := h.answer(ctx, req)
	if ctx.Err() != nil { // shutting down: let JetStream deliver it again
		h.Log.WarnContext(ctx, "shutting down before answering, the request will be redelivered", "request_id", req.RequestID)
		return ctx.Err()
	}
	if err := h.Pub.Publish(ctx, subjectOut, answer); err != nil {
		h.Log.ErrorContext(ctx, "answer not sent", "request_id", req.RequestID, "subject", subjectOut, "err", err)
		return err
	}
	h.Log.InfoContext(ctx, "menu request answered", "request_id", req.RequestID, "subject", subjectOut)
	h.remember(env.ID)
	return nil
}

// answer fetches every day of the request and builds the reply.
func (h *Handler) answer(ctx context.Context, req contracts.MenuRequested) (any, string) {
	ctx = applog.With(ctx, "request_id", req.RequestID)
	start := time.Now()
	h.Log.InfoContext(ctx, "menu requested", "from", req.From, "to", req.To)
	fail := func(code, format string, args ...any) (any, string) {
		msg := fmt.Sprintf(format, args...)
		h.Log.WarnContext(ctx, "menu request failed", "request_id", req.RequestID, "from", req.From, "to", req.To, "code", code, "msg", msg)
		return contracts.MenuError{RequestID: req.RequestID, Code: code, Message: msg}, contracts.SubjectMenuError
	}

	from, err := time.ParseInLocation("2006-01-02", req.From, h.Loc)
	if err != nil {
		return fail(contracts.ErrBadRequest, "from=%q nu e o dată AAAA-LL-ZZ", req.From)
	}
	to := from
	if req.To != "" {
		if to, err = time.ParseInLocation("2006-01-02", req.To, h.Loc); err != nil {
			return fail(contracts.ErrBadRequest, "to=%q nu e o dată AAAA-LL-ZZ", req.To)
		}
	}
	if to.Before(from) {
		return fail(contracts.ErrBadRequest, "to (%s) e înainte de from (%s)", req.To, req.From)
	}
	if days := int(to.Sub(from).Hours()/24) + 1; days > MaxDays {
		return fail(contracts.ErrBadRequest, "perioada are %d zile, cel mult %d", days, MaxDays)
	}

	out := contracts.MenuFetched{RequestID: req.RequestID, Source: contracts.SourceName}
	var empty []string
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		dayStart := time.Now()
		res, err := h.Menu.Get(ctx, d)
		h.Log.DebugContext(ctx, "menu day fetched", "date", date, "dishes", len(res.Items), "cached", res.Cached, "ms", applog.Since(dayStart), "err", err)
		switch {
		case err == nil:
			out.Days = append(out.Days, contracts.Day{Date: date, Items: toItems(res.Items)})
			h.Log.InfoContext(ctx, "menu found", "request_id", req.RequestID, "date", date, "dishes", len(res.Items), "cached", res.Cached)
		case errors.Is(err, menu.ErrEmpty): // also "throttled, and last time it was empty"
			h.Log.InfoContext(ctx, "no menu for the day yet", "date", date, "err", err)
			empty = append(empty, date)
		case errors.Is(err, menu.ErrParseFailed):
			return fail(contracts.ErrParseFailed, "%s: pagina nu mai arată cum o știe parserul (site schimbat?): %v", date, err)
		default: // the site is down, a timeout, or throttled after such an error
			return fail(contracts.ErrSourceUnavailable, "%s: %v", date, err)
		}
	}
	if len(out.Days) == 0 {
		return fail(contracts.ErrNoMenu, "încă nu e meniu pentru %v", empty)
	}
	if len(empty) > 0 { // e.g. a weekend inside the period: the days that have a menu go
		h.Log.InfoContext(ctx, "some days have no menu", "request_id", req.RequestID, "days", empty)
	}
	h.Log.InfoContext(ctx, "menu ready to send", "days", len(out.Days), "ms", applog.Since(start))
	return out, contracts.SubjectMenuFetched
}

func toItems(in []menu.Item) []contracts.Item {
	out := make([]contracts.Item, 0, len(in))
	for _, it := range in {
		ci := contracts.Item{Name: it.Name, Category: it.Category, WeightText: it.Weight, ImageURL: it.ImageURL, Components: it.Components}
		if it.Price > 0 {
			p := it.Price
			ci.Price = &p
		}
		out = append(out, ci)
	}
	return out
}

// how long an envelope id is remembered (JetStream redelivers within minutes)
const keepSeen = time.Hour

func (h *Handler) duplicate(id string) bool {
	if id == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.seen[id]
	return ok
}

func (h *Handler) remember(id string) {
	if id == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.Now()
	for k, t := range h.seen {
		if now.Sub(t) > keepSeen {
			delete(h.seen, k)
		}
	}
	h.seen[id] = now
}
