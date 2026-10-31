// Package scheduler asks core for the automatic recommendation of every
// active user at its order time, on its order days. The menu itself is still
// requested by core's own scheduler; this one only decides *when* a user
// gets the recommendation, because the order time and days live in the bot.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"food-order/bot/internal/applog"
	"food-order/bot/internal/store"
)

// Window: after the order time, the scheduler still tries for this long
// (a bot that was down at 16:00 and starts at 16:40 still sends it).
const Window = 2 * time.Hour

// Store is what the scheduler needs from the database.
type Store interface {
	ActiveUsers(ctx context.Context) ([]store.UserSettings, error)
	ClaimReminder(ctx context.Context, userID int64, forDate string) (attempt int, ok bool, err error)
	FinishReminder(ctx context.Context, userID int64, forDate string, ok bool) error
}

// Core asks for a recommendation; the result comes later as an event.
type Core interface {
	RequestRecommendation(ctx context.Context, userID int64, date string) error
}

// Notify tells the user something (used only when every attempt failed).
type Notify func(ctx context.Context, u store.User, text string)

// Scheduler checks every minute.
type Scheduler struct {
	Store  Store
	Core   Core
	Notify Notify
	Log    *slog.Logger
	Now    func() time.Time
}

// Run checks once a minute until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	if s.Now == nil {
		s.Now = time.Now
	}
	s.Tick(ctx)
	for {
		now := s.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
			s.Tick(ctx)
		}
	}
}

// Tick does one check (exported for tests).
func (s *Scheduler) Tick(ctx context.Context) {
	start := time.Now()
	ctx = applog.With(ctx, "job", "scheduler", "tick_at", start.Truncate(time.Minute))
	due, asked := 0, 0
	defer func() {
		s.Log.DebugContext(ctx, "scheduler tick done", "due", due, "asked", asked, "ms", applog.Since(start))
	}()
	users, err := s.Store.ActiveUsers(ctx)
	if err != nil {
		s.Log.ErrorContext(ctx, "scheduler: users", "err", err)
		return
	}
	now := s.Now()
	s.Log.DebugContext(ctx, "scheduler tick", "active_users", len(users))
	for _, us := range users {
		forDate, isDue := us.Settings.Due(now, Window)
		if !isDue {
			continue
		}
		due++
		date := forDate.Format("2006-01-02")
		uctx := applog.With(ctx, "user_id", us.User.ID, "for", date)
		attempt, ok, err := s.Store.ClaimReminder(ctx, us.User.ID, date)
		if err != nil {
			s.Log.ErrorContext(ctx, "scheduler: claim", "user_id", us.User.ID, "err", err)
			continue
		}
		if !ok {
			s.Log.DebugContext(uctx, "scheduler: already asked for this day")
			continue // already asked (or gave up) for this day
		}
		asked++
		s.Log.DebugContext(uctx, "scheduler: asking core", "attempt", attempt)

		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = s.Core.RequestRecommendation(applog.With(cctx, "user_id", us.User.ID), us.User.ID, date)
		cancel()
		if ferr := s.Store.FinishReminder(ctx, us.User.ID, date, err == nil); ferr != nil {
			s.Log.ErrorContext(ctx, "scheduler: finish", "user_id", us.User.ID, "err", ferr)
		}
		if err == nil {
			s.Log.InfoContext(ctx, "scheduler: recommendation requested", "user_id", us.User.ID, "for", date)
			continue
		}
		s.Log.WarnContext(ctx, "scheduler: request failed", "user_id", us.User.ID, "for", date, "attempt", attempt, "err", err)
		if attempt >= store.MaxReminderAttempts && s.Notify != nil {
			s.Log.ErrorContext(ctx, "scheduler: gave up, the user is told", "user_id", us.User.ID, "for", date, "attempts", attempt)
			s.Notify(ctx, us.User, "❌ Nu am putut cere recomandarea automată pentru "+date+
				". Încearcă /meniu puțin mai târziu.")
		}
	}
}
