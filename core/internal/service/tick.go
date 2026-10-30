package service

import (
	"context"
	"errors"
	"time"

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
	for _, st := range steps {
		if ctx.Err() != nil {
			return
		}
		if err := st.fn(ctx); err != nil {
			s.Log.Error("scheduler", "step", st.name, "err", err)
		}
	}
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
		return nil
	}
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
	for _, rec := range recs {
		ready, err := s.menuReady(ctx, rec.Date)
		if err != nil {
			return err
		}
		if !ready {
			continue
		}
		if err := s.generate(ctx, rec); err != nil {
			s.Log.Warn("generate (retry)", "rec", rec.ID, "err", err)
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
		s.Log.Warn("llm did not answer in time, picks from the score", "rec", rec.ID, "request_id", rec.RequestID)
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
	for _, o := range orders {
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
	return s.Store.ForgetProcessed(ctx, s.Now().Add(-s.Cfg.NATSMaxAge-24*time.Hour))
}
