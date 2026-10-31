package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"food-order/core/internal/applog"
	"food-order/core/internal/bus"
	"food-order/core/internal/contracts"
	"food-order/core/internal/rules"
	"food-order/core/internal/store"
)

// RequestRecommendation is POST /v1/recommendations: the bot asks (at the
// user's order time, or on /meniu) for the recommendation of a day. If the
// menu is ready it is generated now; otherwise it waits for food.menu.ready
// and core asks for the menu if nobody did. The result goes to the bot as
// food.recommendation.ready.
func (s *Service) RequestRecommendation(ctx context.Context, userID int64, date string) (contracts.Recommendation, error) {
	s.Log.InfoContext(ctx, "recommendation requested", "user_id", userID, "date", date)
	d, err := parseDate("date", date)
	if err != nil {
		return contracts.Recommendation{}, err
	}
	today, _ := time.Parse(dateFormat, s.today())
	switch {
	case d.Before(today):
		return contracts.Recommendation{}, badRequest("past_date", "Ziua %s a trecut.", date)
	case d.After(today.AddDate(0, 0, 31)):
		return contracts.Recommendation{}, badRequest("bad_date", "Ziua %s e prea departe.", date)
	}

	// one in progress is enough (the bot's scheduler and /meniu may meet)
	if cur, err := s.Store.LatestRec(ctx, userID, date); err == nil && cur.Status != store.RecReady {
		s.Log.InfoContext(ctx, "a recommendation is already in progress", "user_id", userID, "date", date, "rec", cur.ID, "status", cur.Status)
		// but if it waits for a menu nobody is asking for any more (the
		// request failed, e.g. parse_failed), the user asking again asks for
		// the menu again; a request still in flight is not doubled
		if cur.Status == store.RecWaitingMenu {
			if err := s.ensureMenu(ctx, date, true); err != nil {
				return contracts.Recommendation{}, err
			}
		}
		return s.view(cur), nil
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return contracts.Recommendation{}, err
	}

	rec, err := s.Store.CreateRec(ctx, userID, date, bus.NewID(s.Now()), s.Now())
	if err != nil {
		return contracts.Recommendation{}, err
	}
	ready, err := s.menuReady(ctx, date)
	if err != nil {
		return contracts.Recommendation{}, err
	}
	s.Log.InfoContext(ctx, "recommendation created", "rec", rec.ID, "request_id", rec.RequestID, "user_id", userID, "date", date, "menu_ready", ready)
	if ready {
		if err := s.generate(ctx, rec); err != nil {
			s.Log.WarnContext(ctx, "generate", "rec", rec.ID, "err", err) // stays waiting_menu; the scheduler tries again
		}
	} else if err := s.ensureMenu(ctx, date, true); err != nil {
		return contracts.Recommendation{}, err
	}
	if rec, err = s.Store.RecByID(ctx, rec.ID); err != nil {
		return contracts.Recommendation{}, err
	}
	return s.view(rec), nil
}

// Recommendation is GET /v1/recommendations?date=: the last one of the day.
func (s *Service) Recommendation(ctx context.Context, userID int64, date string) (contracts.Recommendation, error) {
	if _, err := parseDate("date", date); err != nil {
		return contracts.Recommendation{}, err
	}
	rec, err := s.Store.LatestRec(ctx, userID, date)
	if errors.Is(err, store.ErrNotFound) {
		s.Log.DebugContext(ctx, "no recommendation for the day", "user_id", userID, "date", date)
		return contracts.Recommendation{}, notFound("not_found", "Nu am recomandare pentru ziua asta.")
	}
	if err != nil {
		return contracts.Recommendation{}, err
	}
	s.Log.DebugContext(ctx, "recommendation read", "user_id", userID, "date", date, "rec", rec.ID, "status", rec.Status, "picks", len(rec.Picks))
	return s.view(rec), nil
}

func (s *Service) view(rec store.Rec) contracts.Recommendation {
	return contracts.Recommendation{Date: rec.Date, Status: rec.Status, Picks: picksView(rec)}
}

// picksView turns the saved picks into what the bot shows.
func picksView(rec store.Rec) []contracts.Pick {
	byID := make(map[int64]store.Candidate, len(rec.Candidates))
	for _, c := range rec.Candidates {
		byID[c.DishID] = c
	}
	out := make([]contracts.Pick, 0, len(rec.Picks))
	for _, p := range rec.Picks {
		c, ok := byID[p.DishID]
		if !ok {
			c = store.Candidate{DishID: p.DishID, Name: "felul #" + strconv.FormatInt(p.DishID, 10)}
		}
		out = append(out, contracts.Pick{
			DishID: c.DishID, Name: c.Name, Price: c.Price, Reason: p.Reason,
			KcalMin: c.KcalMin, KcalMax: c.KcalMax, AllergensPossible: c.AllergensPossible,
		})
	}
	return out
}

// generate scores the menu of the day for the user and sends the top to
// llm (or, without llm, finishes it from the score alone).
func (s *Service) generate(ctx context.Context, rec store.Rec) error {
	ok, err := s.Store.ClaimRec(ctx, rec.ID, store.RecWaitingMenu, store.RecGenerating, s.Now())
	if err != nil || !ok {
		s.Log.DebugContext(ctx, "recommendation not claimed (someone else is on it)", "rec", rec.ID, "err", err)
		return err // someone else is on it
	}
	ctx = applog.With(ctx, "rec", rec.ID, "request_id", rec.RequestID, "user_id", rec.UserID, "date", rec.Date)
	start := time.Now()
	cands, err := s.candidates(ctx, rec.UserID, rec.Date)
	if err == nil {
		err = s.Store.SetCandidates(ctx, rec.ID, cands)
	}
	if err != nil {
		// back to waiting: the scheduler tries again (e.g. catalog was down)
		if _, rerr := s.Store.ClaimRec(ctx, rec.ID, store.RecGenerating, store.RecWaitingMenu, s.Now()); rerr != nil {
			s.Log.ErrorContext(ctx, "revert recommendation", "rec", rec.ID, "err", rerr)
		}
		s.Log.WarnContext(ctx, "candidates not available, the recommendation waits again", "err", err)
		return err
	}
	s.Log.InfoContext(ctx, "candidates scored", "candidates", len(cands), "ms", applog.Since(start), "top", candidateSummary(cands))
	if len(cands) == 0 || !s.Cfg.LLMEnabled {
		s.Log.InfoContext(ctx, "picks from the score only", "candidates", len(cands), "llm_enabled", s.Cfg.LLMEnabled)
		return s.finish(ctx, rec.ID, s.scorePicks(cands))
	}

	ctxLLM, err := s.llmContext(ctx, rec.UserID, rec.Date)
	if err != nil {
		return err
	}
	req := contracts.RecommendationRequested{RequestID: rec.RequestID, Date: rec.Date, Context: ctxLLM}
	for _, c := range cands {
		req.Candidates = append(req.Candidates, contracts.Candidate{
			ID: c.DishID, Name: c.Name, Price: c.Price, PortionG: c.PortionG,
			KcalMin: c.KcalMin, KcalMax: c.KcalMax, ProteinGMin: c.ProteinGMin, ProteinGMax: c.ProteinGMax,
			FatGMin: c.FatGMin, FatGMax: c.FatGMax, CarbsGMin: c.CarbsGMin, CarbsGMax: c.CarbsGMax,
			Tags: nonNil(c.Tags), AvgRating: c.AvgRating,
		})
	}
	s.Log.DebugContext(ctx, "llm context", "context", ctxLLM)
	if err := s.publish(ctx, contracts.SubjectRecommendationRequested, req); err != nil {
		s.Log.WarnContext(ctx, "llm request not sent, picks from the score", "err", err)
		return s.finish(ctx, rec.ID, s.scorePicks(cands)) // no broker: picks from the score
	}
	s.Log.InfoContext(ctx, "recommendation sent to llm", "rec", rec.ID, "user_id", rec.UserID, "date", rec.Date, "candidates", len(cands))
	return nil
}

// candidates is the deterministic part: catalog menu -> hard filters -> score -> top N.
func (s *Service) candidates(ctx context.Context, userID int64, date string) ([]store.Candidate, error) {
	dishes, err := s.Catalog.Menu(ctx, date)
	if err != nil {
		return nil, err
	}
	s.Log.DebugContext(ctx, "menu from catalog", "dishes", len(dishes))
	prefs, err := s.Store.Prefs(ctx, userID)
	if err != nil {
		return nil, err
	}
	orders, err := s.Store.Orders(ctx, userID, addDays(date, -s.Cfg.RecHistoryDays), addDays(date, -1))
	if err != nil {
		return nil, err
	}

	in := rules.Input{Date: date, Dishes: dishes, Prefs: prefsView(prefs)}
	liked := map[int64]bool{}
	for _, o := range orders {
		in.History = append(in.History, rules.Past{
			DishID: o.DishID, Name: o.DishName, Date: o.Date, Rating: o.Rating, Forced: o.Forced, Labels: o.Labels,
		})
		if o.Rating != nil && *o.Rating >= 4 {
			liked[o.DishID] = true
		}
	}
	if len(liked) > 0 && len(dishes) > 0 && s.Cfg.Score.Weights.Similarity > 0 {
		var likedIDs, candIDs []int64
		for id := range liked {
			likedIDs = append(likedIDs, id)
		}
		sort.Slice(likedIDs, func(i, j int) bool { return likedIDs[i] < likedIDs[j] })
		for _, d := range dishes {
			candIDs = append(candIDs, d.ID)
		}
		if sim, err := s.Catalog.Similar(ctx, likedIDs, candIDs); err == nil {
			in.Similarity = sim
		} else {
			s.Log.WarnContext(ctx, "similarity not available, scoring without it", "err", err)
		}
	}

	s.Log.DebugContext(ctx, "scoring input", "dishes", len(dishes), "prefs", len(prefs), "history", len(in.History),
		"liked", len(liked), "similarity", len(in.Similarity))
	res := rules.Rank(in, s.Cfg.Score)
	for _, e := range res.Excluded {
		s.Log.DebugContext(ctx, "dish excluded", "user_id", userID, "date", date, "dish_id", e.DishID, "reason", e.Reason)
	}
	for i, r := range res.Ranked {
		s.Log.DebugContext(ctx, "dish scored", "rank", i+1, "dish_id", r.Dish.ID, "dish", r.Dish.Name, "score", r.Score, "avg_rating", r.AvgRating, "parts", r.Parts)
	}
	n := min(len(res.Ranked), s.Cfg.RecCandidates)
	out := make([]store.Candidate, 0, n)
	for _, r := range res.Ranked[:n] {
		out = append(out, candidateOf(r))
	}
	return out, nil
}

func candidateOf(r rules.Scored) store.Candidate {
	d := r.Dish
	snap := snapshotOf(d)
	c := store.Candidate{
		DishID: d.ID, Name: d.Name, Price: d.Price, Score: r.Score, AvgRating: r.AvgRating, Tags: d.Tags,
		PortionG: snap.PortionG, KcalMin: snap.KcalMin, KcalMax: snap.KcalMax,
		ProteinGMin: snap.ProteinGMin, ProteinGMax: snap.ProteinGMax, FatGMin: snap.FatGMin, FatGMax: snap.FatGMax,
		CarbsGMin: snap.CarbsGMin, CarbsGMax: snap.CarbsGMax, NutritionSource: snap.NutritionSource,
	}
	// only the model's guesses nobody confirmed: the bot asks "contains X?"
	if d.Allergens.Confirmed == nil && len(d.Allergens.Given) == 0 {
		c.AllergensPossible = d.Allergens.Possible
	}
	return c
}

// scorePicks is the fallback without llm: the best by score, no reason.
func (s *Service) scorePicks(cands []store.Candidate) []store.PickRef {
	n := min(len(cands), s.Cfg.RecPicks)
	out := make([]store.PickRef, 0, n)
	for _, c := range cands[:n] {
		out = append(out, store.PickRef{DishID: c.DishID})
	}
	return out
}

// finish saves the picks (generating -> ready) and tells the bot.
func (s *Service) finish(ctx context.Context, recID int64, picks []store.PickRef) error {
	ok, err := s.Store.FinishRec(ctx, recID, picks, s.Now())
	if err != nil || !ok {
		s.Log.DebugContext(ctx, "recommendation not finished (already finished?)", "rec", recID, "err", err)
		return err
	}
	s.Log.DebugContext(ctx, "recommendation finished", "rec", recID, "picks", picks)
	return s.notify(ctx, recID)
}

// notify publishes food.recommendation.ready; if the broker is down the
// scheduler publishes it later (notified_at stays empty).
func (s *Service) notify(ctx context.Context, recID int64) error {
	rec, err := s.Store.RecByID(ctx, recID)
	if err != nil {
		return err
	}
	ev := contracts.RecommendationReady{UserID: rec.UserID, Date: rec.Date, Picks: picksView(rec)}
	if err := s.publish(ctx, contracts.SubjectRecommendationReady, ev); err != nil {
		return nil // kept for later
	}
	s.Log.InfoContext(ctx, "recommendation ready", "rec", rec.ID, "user_id", rec.UserID, "date", rec.Date, "picks", len(ev.Picks))
	return s.Store.MarkNotified(ctx, rec.ID, s.Now())
}

// OnRecommendationGenerated handles the answer of llm: only ids from the
// list are kept, in llm's order; nothing valid => picks from the score.
func (s *Service) OnRecommendationGenerated(ctx context.Context, ev contracts.RecommendationGenerated) error {
	rec, err := s.Store.RecByRequestID(ctx, ev.RequestID)
	if errors.Is(err, store.ErrNotFound) {
		s.Log.WarnContext(ctx, "llm answer for an unknown request", "request_id", ev.RequestID)
		return nil
	}
	if err != nil {
		return err
	}
	if rec.Status != store.RecGenerating {
		s.Log.InfoContext(ctx, "late llm answer ignored", "rec", rec.ID, "status", rec.Status, "request_id", ev.RequestID)
		return nil // already finished (e.g. by the llm timeout)
	}
	s.Log.InfoContext(ctx, "llm answered", "rec", rec.ID, "user_id", rec.UserID, "date", rec.Date, "picks", len(ev.Picks))
	known := map[int64]bool{}
	for _, c := range rec.Candidates {
		known[c.DishID] = true
	}
	var picks []store.PickRef
	seen := map[int64]bool{}
	for _, p := range ev.Picks {
		if !known[p.ID] || seen[p.ID] || len(picks) >= s.Cfg.RecPicks {
			s.Log.DebugContext(ctx, "llm pick dropped", "dish_id", p.ID, "known", known[p.ID], "duplicate", seen[p.ID], "reason", p.Reason)
			continue
		}
		seen[p.ID] = true
		picks = append(picks, store.PickRef{DishID: p.ID, Reason: cut(strings.TrimSpace(p.Reason), 300)})
	}
	if len(picks) == 0 {
		s.Log.WarnContext(ctx, "llm gave no valid pick, using the score", "rec", rec.ID)
		picks = s.scorePicks(rec.Candidates)
	}
	return s.finish(ctx, rec.ID, picks)
}

// llmContext is the short context for the model: food facts only.
func (s *Service) llmContext(ctx context.Context, userID int64, date string) (contracts.RecommendationContext, error) {
	out := contracts.RecommendationContext{
		MaxPicks: s.Cfg.RecPicks, Recent: []contracts.ContextOrder{}, Liked: []string{}, Disliked: []string{},
		Likes: []string{}, Avoid: []string{},
	}
	recent, err := s.Store.Orders(ctx, userID, addDays(date, -7), addDays(date, -1))
	if err != nil {
		return out, err
	}
	for _, o := range recent {
		out.Recent = append(out.Recent, contracts.ContextOrder{Date: o.Date, Name: o.DishName, Rating: o.Rating, Forced: o.Forced})
	}

	history, err := s.Store.Orders(ctx, userID, addDays(date, -s.Cfg.RecHistoryDays), addDays(date, -1))
	if err != nil {
		return out, err
	}
	type agg struct {
		name   string
		sum, n int
	}
	by := map[int64]*agg{}
	for _, o := range history {
		if o.Rating == nil {
			continue
		}
		a := by[o.DishID]
		if a == nil {
			a = &agg{}
			by[o.DishID] = a
		}
		a.name, a.sum, a.n = o.DishName, a.sum+*o.Rating, a.n+1
	}
	list := make([]*agg, 0, len(by))
	for _, a := range by {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		ai, aj := float64(list[i].sum)/float64(list[i].n), float64(list[j].sum)/float64(list[j].n)
		if ai != aj {
			return ai > aj
		}
		return list[i].name < list[j].name
	})
	for _, a := range list {
		avg := float64(a.sum) / float64(a.n)
		if avg >= 4 && len(out.Liked) < 5 {
			out.Liked = append(out.Liked, a.name)
		}
	}
	for i := len(list) - 1; i >= 0; i-- {
		if avg := float64(list[i].sum) / float64(list[i].n); avg <= 2 && len(out.Disliked) < 5 {
			out.Disliked = append(out.Disliked, list[i].name)
		}
	}

	prefs, err := s.Store.Prefs(ctx, userID)
	if err != nil {
		return out, err
	}
	for _, p := range prefs {
		switch {
		case p.Kind == rules.KindLike, p.Kind == rules.KindDiet && !p.Hard:
			out.Likes = append(out.Likes, p.Value)
		case p.Kind == rules.KindDislike && !p.Hard:
			out.Avoid = append(out.Avoid, p.Value)
		}
	}

	month, err := s.Store.Orders(ctx, userID, date[:8]+"01", date)
	if err != nil {
		return out, err
	}
	for _, o := range month {
		if o.Price != nil {
			out.MonthSpent += *o.Price
		}
	}
	return out, nil
}

// candidateSummary is the short form of candidates for the logs.
func candidateSummary(cands []store.Candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, strconv.FormatInt(c.DishID, 10)+" "+c.Name+" ("+strconv.FormatFloat(c.Score, 'f', 3, 64)+")")
	}
	return out
}

func prefsView(ps []store.Pref) []contracts.Pref {
	out := make([]contracts.Pref, 0, len(ps))
	for _, p := range ps {
		out = append(out, contracts.Pref{ID: p.ID, Kind: p.Kind, Value: p.Value, Hard: p.Hard})
	}
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}
