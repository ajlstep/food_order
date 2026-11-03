// Package service is core's business logic: it asks for the menu, keeps
// orders and ratings per user_id, scores the dishes and
// orchestrates the recommendation (catalog -> score -> llm -> person). The
// rpc handlers, the event handlers and the scheduler only call into this package.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"food-order/core/internal/config"
	"food-order/core/internal/store"
	"food-order/shared/contracts/catalog"
	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
)

// Catalog is what core asks catalog (implemented by remote.Client).
type Catalog interface {
	Menu(ctx context.Context, date string) ([]catalog.Dish, error)
	Dishes(ctx context.Context, ids []int64, date string) ([]catalog.Dish, error)
	Similar(ctx context.Context, liked, candidates []int64) (map[int64]float64, error)
}

// Users is what core asks users (implemented by remote.Client).
type Users interface {
	Food(ctx context.Context, userID string) (users.FoodResponse, error)
	Due(ctx context.Context, at time.Time, window time.Duration) ([]users.DueItem, error)
	Snapshot(ctx context.Context, userID string) (users.SnapshotResponse, error)
	Company(ctx context.Context, userID string) (users.Company, error)
}

// Service holds the dependencies of the business logic.
type Service struct {
	Store   *store.Store
	Catalog Catalog
	Users   Users
	Bus     events.Publisher
	Cfg     config.Config
	Log     *slog.Logger
	Now     func() time.Time
}

// New makes a Service with time.Now as the clock.
func New(st *store.Store, cat Catalog, people Users, pub events.Publisher, cfg config.Config, log *slog.Logger) *Service {
	return &Service{Store: st, Catalog: cat, Users: people, Bus: pub, Cfg: cfg, Log: log, Now: time.Now}
}

// Error is a refusal: a stable code and the words for the person, in Romanian.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func badRequest(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func notFound(code, msg string) error { return &Error{Code: code, Message: msg} }

// --------------------------------------------------------------- dates

const dateFormat = store.DateFormat

// today is the current day in the configured time zone.
func (s *Service) today() string { return s.Now().In(s.Cfg.Location).Format(dateFormat) }

func parseDate(field, v string) (time.Time, error) {
	t, err := time.Parse(dateFormat, v)
	if err != nil {
		return time.Time{}, badRequest("bad_date", "%s: data trebuie scrisă ca AAAA-LL-ZZ (de ex. 2026-10-28)", field)
	}
	return t, nil
}

func addDays(date string, n int) string {
	t, err := time.Parse(dateFormat, date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, n).Format(dateFormat)
}

// days returns every day from from to to (inclusive), at most max days.
func days(from, to string, max int) []string {
	f, err1 := time.Parse(dateFormat, from)
	t, err2 := time.Parse(dateFormat, to)
	if err1 != nil || err2 != nil || t.Before(f) {
		return []string{from}
	}
	var out []string
	for d := f; !d.After(t) && len(out) < max; d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format(dateFormat))
	}
	return out
}

// period reads ?from=&to= with defaults (last 30 days up to today).
func (s *Service) period(from, to string, maxDays int) (string, string, error) {
	if to == "" {
		to = s.today()
	}
	if from == "" {
		from = addDays(to, -30)
	}
	f, err := parseDate("from", from)
	if err != nil {
		return "", "", err
	}
	t, err := parseDate("to", to)
	if err != nil {
		return "", "", err
	}
	if t.Before(f) {
		return "", "", badRequest("bad_period", "„from” trebuie să fie înainte de „to”")
	}
	if t.Sub(f) > time.Duration(maxDays)*24*time.Hour {
		return "", "", badRequest("bad_period", "perioada e prea lungă (cel mult %d zile)", maxDays)
	}
	return from, to, nil
}

// --------------------------------------------------------------- events out

// publish sends an event and logs a failure; it returns the error.
func (s *Service) publish(ctx context.Context, subject string, data any) error {
	err := s.Bus.Publish(ctx, subject, data)
	if err != nil {
		s.Log.WarnContext(ctx, "publish failed", "subject", subject, "err", err)
	}
	return err
}

// alert sends food.alert to the admin chat (through the bot) and logs it.
func (s *Service) alert(ctx context.Context, level, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	if level == "error" {
		s.Log.ErrorContext(ctx, "alert to the admins", "alert_level", level, "text", text)
	} else {
		s.Log.WarnContext(ctx, "alert to the admins", "alert_level", level, "text", text)
	}
	_ = s.publish(ctx, food.SubjectAlert, food.Alert{Service: "core", Level: level, Text: text})
}
