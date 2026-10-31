package service

import (
	"context"
	"errors"
	"time"

	"food-order/core/internal/bus"
	"food-order/core/internal/contracts"
	"food-order/core/internal/store"
)

// The menu is shared: one request for a day serves every user. Core only
// asks (food.menu.requested) and gets back food.menu.ready from catalog or
// food.menu.error from the source; it does not know whether the source is
// the parser or, later, the public api.

// menuReady says whether the menu of the day is in catalog.
func (s *Service) menuReady(ctx context.Context, date string) (bool, error) {
	return s.Store.MenuReadyFor(ctx, date, "")
}

// ensureMenu asks for the menu of a day unless it is ready or already being
// asked. A finished (failed) request is asked again only on demand (a user
// wants a recommendation), not by the daily schedule.
func (s *Service) ensureMenu(ctx context.Context, date string, onDemand bool) error {
	ready, err := s.menuReady(ctx, date)
	if err != nil || ready {
		if ready {
			s.Log.DebugContext(ctx, "menu already ready", "date", date)
		}
		return err
	}
	m, err := s.Store.LatestMenuFor(ctx, date)
	switch {
	case err == nil && m.Active():
		s.Log.DebugContext(ctx, "menu already being asked for", "date", date, "request_id", m.RequestID, "status", m.Status, "attempts", m.Attempts)
		return nil
	case err == nil && !onDemand:
		s.Log.DebugContext(ctx, "menu request finished without a menu; the schedule does not ask again", "date", date,
			"request_id", m.RequestID, "status", m.Status, "error_code", m.ErrorCode)
		return nil
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return err
	}
	now := s.Now()
	m = store.MenuRequest{
		RequestID: bus.NewID(now), DateFrom: date, DateTo: date, Status: store.MenuRequested,
		Attempts: 1, RequestedAt: now.UTC().Format(store.TimeFormat),
	}
	if err := s.Store.CreateMenuRequest(ctx, m, now); err != nil {
		return err
	}
	s.Log.InfoContext(ctx, "menu requested", "request_id", m.RequestID, "date", date, "on_demand", onDemand)
	// if this fails the request expires after CORE_MENU_TIMEOUT and is resent
	_ = s.publish(ctx, contracts.SubjectMenuRequested, contracts.MenuRequested{RequestID: m.RequestID, From: date, To: date})
	return nil
}

// resendMenu sends a request again (same request_id, so a late answer to an
// earlier send still counts).
func (s *Service) resendMenu(ctx context.Context, m store.MenuRequest) error {
	now := s.Now()
	m.Attempts++
	m.Status = store.MenuRequested
	m.RequestedAt = now.UTC().Format(store.TimeFormat)
	m.NextRetryAt = ""
	if err := s.Store.UpdateMenuRequest(ctx, m, now); err != nil {
		return err
	}
	s.Log.InfoContext(ctx, "menu requested again", "request_id", m.RequestID, "date", m.DateFrom, "attempt", m.Attempts)
	_ = s.publish(ctx, contracts.SubjectMenuRequested, contracts.MenuRequested{RequestID: m.RequestID, From: m.DateFrom, To: m.DateTo})
	return nil
}

// OnMenuReady handles food.menu.ready: the menu is in catalog, so every
// recommendation waiting for those days can be generated now.
func (s *Service) OnMenuReady(ctx context.Context, ev contracts.MenuReady) error {
	if ev.To == "" {
		ev.To = ev.From
	}
	if _, err := parseDate("from", ev.From); err != nil {
		return err
	}
	dates := days(ev.From, ev.To, 31)
	now := s.Now()

	// a ready menu for the same day from another request = a new revision
	revision := map[string]bool{}
	for _, d := range dates {
		r, err := s.Store.MenuReadyFor(ctx, d, ev.RequestID)
		if err != nil {
			return err
		}
		revision[d] = r
	}

	m, err := s.Store.MenuRequestByID(ctx, ev.RequestID)
	switch {
	case errors.Is(err, store.ErrNotFound): // e.g. the api pushed a menu nobody asked for
		err = s.Store.CreateMenuRequest(ctx, store.MenuRequest{
			RequestID: ev.RequestID, DateFrom: ev.From, DateTo: ev.To, Status: store.MenuReady,
		}, now)
	case err == nil:
		m.Status, m.ErrorCode, m.ErrorMessage, m.NextRetryAt = store.MenuReady, "", "", ""
		m.DateFrom, m.DateTo = ev.From, ev.To
		err = s.Store.UpdateMenuRequest(ctx, m, now)
	}
	if err != nil {
		return err
	}
	s.Log.InfoContext(ctx, "menu ready", "request_id", ev.RequestID, "from", ev.From, "to", ev.To, "dishes", len(ev.DishIDs))

	for _, d := range dates {
		waiting, err := s.Store.RecsWaitingFor(ctx, d)
		if err != nil {
			return err
		}
		for _, rec := range waiting {
			if err := s.generate(ctx, rec); err != nil {
				s.Log.WarnContext(ctx, "generate after menu ready", "rec", rec.ID, "err", err) // the scheduler tries again
			}
		}
		s.Log.DebugContext(ctx, "recommendations waiting for this menu", "date", d, "count", len(waiting), "revision", revision[d])
		if revision[d] && s.Cfg.RegenerateOnRevision {
			s.Log.InfoContext(ctx, "new menu revision, regenerating recommendations", "date", d)
			s.regenerate(ctx, d)
		}
	}
	return nil
}

// regenerate makes a new recommendation for every user whose last one for
// the day is ready and who has not chosen yet.
func (s *Service) regenerate(ctx context.Context, date string) {
	recs, err := s.Store.LatestReadyRecsFor(ctx, date)
	if err != nil {
		s.Log.WarnContext(ctx, "regenerate", "date", date, "err", err)
		return
	}
	for _, old := range recs {
		chose, err := s.Store.HasOrder(ctx, old.UserID, date)
		if err != nil || chose {
			s.Log.DebugContext(ctx, "regenerate skipped", "user_id", old.UserID, "date", date, "already_chose", chose, "err", err)
			continue
		}
		rec, err := s.Store.CreateRec(ctx, old.UserID, date, bus.NewID(s.Now()), s.Now())
		if err == nil {
			err = s.generate(ctx, rec)
		}
		if err != nil {
			s.Log.WarnContext(ctx, "regenerate", "user_id", old.UserID, "date", date, "err", err)
		}
	}
}

// OnMenuError handles food.menu.error from the source.
func (s *Service) OnMenuError(ctx context.Context, ev contracts.MenuError) error {
	m, err := s.Store.MenuRequestByID(ctx, ev.RequestID)
	if errors.Is(err, store.ErrNotFound) {
		s.Log.WarnContext(ctx, "menu error for an unknown request", "request_id", ev.RequestID, "code", ev.Code)
		return nil
	}
	if err != nil {
		return err
	}
	if m.Status == store.MenuReady {
		s.Log.DebugContext(ctx, "late menu error ignored, the menu is ready", "request_id", ev.RequestID, "code", ev.Code)
		return nil // a late error after the menu came
	}
	s.Log.WarnContext(ctx, "menu source answered with an error", "request_id", ev.RequestID, "date", m.DateFrom,
		"code", ev.Code, "message", ev.Message, "attempts", m.Attempts)
	return s.menuFailed(ctx, m, ev.Code, ev.Message, store.MenuError)
}

// menuFailed decides what to do after an error (table in the Core note):
//
//	no_menu            retry every CORE_MENU_RETRY_INTERVAL until the deadline, then alert
//	source_unavailable retry with a growing pause; alert after CORE_MENU_MAX_ATTEMPTS
//	parse_failed       alert at once, no retries (the site changed)
//	bad_request        alert (a bug)
//
// A request without an answer in CORE_MENU_TIMEOUT counts as source_unavailable.
func (s *Service) menuFailed(ctx context.Context, m store.MenuRequest, code, msg, status string) error {
	now := s.Now()
	m.Status, m.ErrorCode, m.ErrorMessage, m.NextRetryAt = status, code, msg, ""
	deadline := s.deadline(m.DateFrom)
	retryAt := func(next time.Time) bool {
		if next.Before(deadline) {
			m.NextRetryAt = next.UTC().Format(store.TimeFormat)
			return true
		}
		return false
	}

	switch code {
	case contracts.MenuErrNoMenu:
		if !retryAt(now.Add(s.Cfg.MenuRetryInterval)) && !m.Alerted {
			m.Alerted = true
			s.alert(ctx, "warn", "Meniul pentru %s încă nu a apărut, iar ora limită (%s) a trecut. Nu mai reîncerc.",
				m.DateFrom, deadline.In(s.Cfg.Location).Format("02.01 15:04"))
		}
	case contracts.MenuErrSourceUnavailable:
		will := retryAt(now.Add(s.backoff(m.Attempts)))
		if !m.Alerted && (m.Attempts >= s.Cfg.MenuMaxAttempts || !will) {
			m.Alerted = true
			tail := "Reîncerc în continuare."
			if !will {
				tail = "Ora limită a trecut, nu mai reîncerc."
			}
			s.alert(ctx, "error", "Sursa meniului nu răspunde pentru %s (%d încercări): %s. %s", m.DateFrom, m.Attempts, msg, tail)
		}
	case contracts.MenuErrParseFailed:
		m.Alerted = true
		s.alert(ctx, "error", "Sursa meniului nu mai înțelege pagina pentru %s (site-ul s-a schimbat?): %s. Nu reîncerc.", m.DateFrom, msg)
	default: // bad_request or a code core does not know
		m.Alerted = true
		s.alert(ctx, "error", "Cererea de meniu pentru %s a fost respinsă (%s): %s. Probabil un bug.", m.DateFrom, code, msg)
	}
	s.Log.InfoContext(ctx, "menu error", "request_id", m.RequestID, "date", m.DateFrom, "code", code, "attempts", m.Attempts, "next_retry", m.NextRetryAt)
	return s.Store.UpdateMenuRequest(ctx, m, now)
}

// deadline: on the day before the menu day, at CORE_MENU_DEADLINE.
func (s *Service) deadline(date string) time.Time {
	d, err := time.ParseInLocation(dateFormat, date, s.Cfg.Location)
	if err != nil {
		return s.Now()
	}
	return s.Cfg.MenuDeadline.On(d.AddDate(0, 0, -1), s.Cfg.Location)
}

// backoff: CORE_MENU_BACKOFF, doubled for every send, at most CORE_MENU_BACKOFF_MAX.
func (s *Service) backoff(attempts int) time.Duration {
	d := s.Cfg.MenuBackoff
	for i := 1; i < attempts && d < s.Cfg.MenuBackoffMax; i++ {
		d *= 2
	}
	if d > s.Cfg.MenuBackoffMax {
		d = s.Cfg.MenuBackoffMax
	}
	return d
}

// nextServingDay is the first day after today the kitchen serves (CORE_MENU_DAYS).
func (s *Service) nextServingDay() string {
	t := s.Now().In(s.Cfg.Location)
	for i := 1; i <= 7; i++ {
		d := t.AddDate(0, 0, i)
		wd := int(d.Weekday())
		if wd == 0 {
			wd = 7
		}
		if s.Cfg.MenuDays[wd] {
			return d.Format(dateFormat)
		}
	}
	return ""
}
