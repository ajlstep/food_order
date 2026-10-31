package service

import (
	"context"
	"errors"
	"time"

	"food-order/core/internal/applog"
	"food-order/core/internal/bus"
	"food-order/core/internal/contracts"
	"food-order/core/internal/store"
)

// Tick is one round of the scheduler (the menu scheduler module of core).
// Every step is safe to repeat: the database says what was done already,
// so a restart or a missed tick changes nothing.
func (s *Service) Tick(ctx context.Context) {
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"daily menu", s.dailyMenu},
		{"expire menu requests", s.expireMenus},
		{"retry menu requests", s.retryMenus},
		{"waiting recommendations", s.sweepWaiting},
		{"llm timeouts", s.llmTimeouts},
		{"unsent recommendations", s.republish},
		{"rating questions", s.askRatings},
		{"forget old events", s.forgetEvents},
	}
	tickStart := time.Now()
	ctx = applog.With(ctx, "job", "scheduler", "tick_id", bus.NewID(tickStart))
	for _, st := range steps {
		if ctx.Err() != nil {
			return
		}
		sctx := applog.With(ctx, "step", st.name)
		start := time.Now()
		if err := st.fn(sctx); err != nil {
			s.Log.ErrorContext(sctx, "scheduler step failed", "err", err, "ms", applog.Since(start))
			continue
		}
		s.Log.DebugContext(sctx, "scheduler step done", "ms", applog.Since(start))
	}
	s.Log.DebugContext(ctx, "scheduler tick done", "steps", len(steps), "ms", applog.Since(tickStart))
}

// dailyMenu: from CORE_MENU_REQUEST_TIME on, ask once for the menu of the
// next serving day (Friday -> Monday with the default days).
func (s *Service) dailyMenu(ctx context.Context) error {
	now := s.Now().In(s.Cfg.Location)
	if now.Before(s.Cfg.MenuRequestTime.On(now, s.Cfg.Location)) {
		return nil
	}
	target := s.nextServingDay()
	if target == "" {
		s.Log.DebugContext(ctx, "daily menu: no serving day in the next week", "menu_days", s.Cfg.MenuDays)
		return nil
	}
	s.Log.DebugContext(ctx, "daily menu: checking", "date", target, "request_time", s.Cfg.MenuRequestTime.String())
	return s.ensureMenu(ctx, target, false)
}

// expireMenus: a request without any answer in CORE_MENU_TIMEOUT counts
// as source_unavailable.
func (s *Service) expireMenus(ctx context.Context) error {
	old, err := s.Store.MenusSentBefore(ctx, s.Now().Add(-s.Cfg.MenuTimeout))
	if err != nil {
		return err
	}
	for _, m := range old {
		s.Log.WarnContext(ctx, "menu request expired without an answer", "request_id", m.RequestID, "date", m.DateFrom,
			"attempts", m.Attempts, "requested_at", m.RequestedAt, "timeout", s.Cfg.MenuTimeout)
		msg := "niciun răspuns în " + s.Cfg.MenuTimeout.String()
		if err := s.menuFailed(ctx, m, contracts.MenuErrSourceUnavailable, msg, store.MenuExpired); err != nil {
			return err
		}
	}
	return nil
}

// retryMenus resends the requests whose pause is over.
func (s *Service) retryMenus(ctx context.Context) error {
	due, err := s.Store.MenusToRetry(ctx, s.Now())
	if err != nil {
		return err
	}
	for _, m := range due {
		if err := s.resendMenu(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// sweepWaiting generates the recommendations that wait although the menu
// is ready (an event was missed, or catalog did not answer the first time).
func (s *Service) sweepWaiting(ctx context.Context) error {
	recs, err := s.Store.RecsWaiting(ctx, s.today())
	if err != nil {
		return err
	}
	if len(recs) > 0 {
		s.Log.DebugContext(ctx, "recommendations waiting for a menu", "count", len(recs))
	}
	for _, rec := range recs {
		ready, err := s.menuReady(ctx, rec.Date)
		if err != nil {
			return err
		}
		if !ready {
			s.Log.DebugContext(ctx, "still no menu for the waiting recommendation", "rec", rec.ID, "user_id", rec.UserID, "date", rec.Date)
			continue
		}
		s.Log.InfoContext(ctx, "menu is ready, generating a waiting recommendation", "rec", rec.ID, "user_id", rec.UserID, "date", rec.Date)
		if err := s.generate(ctx, rec); err != nil {
			s.Log.WarnContext(ctx, "generate (retry)", "rec", rec.ID, "user_id", rec.UserID, "date", rec.Date, "err", err)
		}
	}
	return nil
}

// llmTimeouts: llm did not answer in CORE_LLM_TIMEOUT => picks from the
// score, without a reason.
func (s *Service) llmTimeouts(ctx context.Context) error {
	recs, err := s.Store.RecsGeneratingBefore(ctx, s.Now().Add(-s.Cfg.LLMTimeout))
	if err != nil {
		return err
	}
	for _, rec := range recs {
		s.Log.WarnContext(ctx, "llm did not answer in time, picks from the score", "rec", rec.ID, "request_id", rec.RequestID)
		if err := s.finish(ctx, rec.ID, s.scorePicks(rec.Candidates)); err != nil {
			return err
		}
	}
	return nil
}

// republish sends food.recommendation.ready again for the ready ones the
// bot never got (the broker was down when they were finished).
func (s *Service) republish(ctx context.Context) error {
	if _, off := s.Bus.(bus.Off); off {
		return nil
	}
	recs, err := s.Store.RecsUnnotified(ctx, s.today())
	if err != nil {
		return err
	}
	for _, rec := range recs {
		s.Log.InfoContext(ctx, "sending a recommendation the bot never got", "rec", rec.ID, "user_id", rec.UserID, "date", rec.Date)
		if err := s.notify(ctx, rec.ID); err != nil {
			return err
		}
	}
	return nil
}

// askRatings: from CORE_RATING_TIME on, ask about the meals of the last
// CORE_RATING_LOOKBACK_DAYS days that have no rating yet (once per order).
func (s *Service) askRatings(ctx context.Context) error {
	now := s.Now().In(s.Cfg.Location)
	if now.Before(s.Cfg.RatingTime.On(now, s.Cfg.Location)) {
		return nil
	}
	today := s.today()
	orders, err := s.Store.OrdersToAskRating(ctx, addDays(today, -s.Cfg.RatingLookbackDays), addDays(today, -1))
	if err != nil {
		return err
	}
	if len(orders) > 0 {
		s.Log.InfoContext(ctx, "asking for ratings", "orders", len(orders))
	}
	for _, o := range orders {
		s.Log.DebugContext(ctx, "rating question", "order", o.ID, "user_id", o.UserID, "date", o.Date, "dish", o.DishName)
		err := s.publish(ctx, contracts.SubjectRatingRequested, contracts.RatingRequested{UserID: o.UserID, OrderID: o.ID})
		if errors.Is(err, bus.ErrOff) {
			return nil
		}
		if err != nil {
			return nil // broker down: next tick
		}
		if err := s.Store.MarkRatingRequested(ctx, o.ID, s.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) forgetEvents(ctx context.Context) error {
	before := s.Now().Add(-s.Cfg.NATSMaxAge - 24*time.Hour)
	s.Log.DebugContext(ctx, "forgetting processed event ids", "before", before)
	return s.Store.ForgetProcessed(ctx, before)
}
