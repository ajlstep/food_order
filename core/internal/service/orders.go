package service

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"food-order/core/internal/catalog"
	"food-order/core/internal/contracts"
	"food-order/core/internal/rules"
	"food-order/core/internal/store"
)

// CreateOrder is POST /v1/orders: the user pressed a dish in the bot. The
// snapshot (name, portion, nutrition with min and max) is taken from catalog
// now, from the menu of that day; if catalog does not answer, from the
// candidates saved with the user's recommendation of the day.
func (s *Service) CreateOrder(ctx context.Context, userID int64, req contracts.CreateOrderRequest) (contracts.Order, error) {
	if _, err := parseDate("date", req.Date); err != nil {
		return contracts.Order{}, err
	}
	if req.DishID <= 0 {
		return contracts.Order{}, badRequest("bad_dish", "Lipsește felul (dish_id).")
	}
	if req.Price != nil && *req.Price < 0 {
		return contracts.Order{}, badRequest("bad_price", "Prețul nu poate fi negativ.")
	}

	snap, price, err := s.snapshot(ctx, userID, req.DishID, req.Date)
	if err != nil {
		return contracts.Order{}, err
	}
	if req.Price != nil {
		price = req.Price
	}
	o, err := s.Store.SaveOrder(ctx, store.Order{
		UserID: userID, Date: req.Date, DishID: req.DishID, Price: price, Forced: req.Forced, Snapshot: snap,
	}, s.Now())
	if errors.Is(err, store.ErrRated) {
		return contracts.Order{}, &Error{Status: http.StatusConflict, Code: "already_rated",
			Message: "Masa din ziua asta e deja notată, alegerea nu se mai poate schimba."}
	}
	if err != nil {
		return contracts.Order{}, err
	}
	s.Log.Info("order saved", "order", o.ID, "user_id", userID, "date", o.Date, "dish_id", o.DishID, "forced", o.Forced)
	return orderView(o), nil
}

func (s *Service) snapshot(ctx context.Context, userID, dishID int64, date string) (store.Snapshot, *float64, error) {
	dishes, catErr := s.Catalog.Dishes(ctx, []int64{dishID}, date)
	if catErr == nil {
		for _, d := range dishes {
			if d.ID == dishID {
				return snapshotOf(d), d.Price, nil
			}
		}
	}
	// fallback: what we saved when we recommended it
	if rec, err := s.Store.LatestRec(ctx, userID, date); err == nil {
		for _, c := range rec.Candidates {
			if c.DishID == dishID {
				return store.Snapshot{
					DishName: c.Name, PortionG: c.PortionG, KcalMin: c.KcalMin, KcalMax: c.KcalMax,
					ProteinGMin: c.ProteinGMin, ProteinGMax: c.ProteinGMax, FatGMin: c.FatGMin, FatGMax: c.FatGMax,
					CarbsGMin: c.CarbsGMin, CarbsGMax: c.CarbsGMax, NutritionSource: orMissing(c.NutritionSource),
				}, c.Price, nil
			}
		}
	}
	if catErr != nil && !errors.Is(catErr, catalog.ErrNotFound) {
		s.Log.Warn("catalog for snapshot", "dish_id", dishID, "err", catErr)
		return store.Snapshot{}, nil, &Error{Status: http.StatusServiceUnavailable, Code: "catalog_unavailable",
			Message: "Catalogul nu răspunde acum. Încearcă puțin mai târziu."}
	}
	return store.Snapshot{}, nil, &Error{Status: http.StatusUnprocessableEntity, Code: "dish_not_in_menu",
		Message: "Felul nu e în meniul zilei."}
}

// snapshotOf takes from a catalog dish what goes into orders: name,
// portion and kcal/protein/fat/carbs, each with min and max.
func snapshotOf(d contracts.CatalogDish) store.Snapshot {
	snap := store.Snapshot{DishName: d.Name, PortionG: d.WeightG, NutritionSource: orMissing(d.NutritionSource)}
	if snap.DishName == "" {
		snap.DishName = "felul #" + strconv.FormatInt(d.ID, 10)
	}
	n := d.Nutrition
	if n == nil || snap.NutritionSource == "missing" {
		snap.NutritionSource = "missing"
		return snap
	}
	snap.KcalMin, snap.KcalMax = rangeOf(n.Kcal)
	snap.ProteinGMin, snap.ProteinGMax = rangeOf(n.ProteinG)
	snap.FatGMin, snap.FatGMax = rangeOf(n.FatG)
	snap.CarbsGMin, snap.CarbsGMax = rangeOf(n.CarbsG)
	if snap.KcalMin == nil && snap.ProteinGMin == nil && snap.FatGMin == nil && snap.CarbsGMin == nil {
		snap.NutritionSource = "missing"
	}
	return snap
}

func rangeOf(r *contracts.Range) (*float64, *float64) {
	if r == nil {
		return nil, nil
	}
	lo, hi := r.Min, r.Max
	if lo > hi {
		lo, hi = hi, lo
	}
	return &lo, &hi
}

func orMissing(s string) string {
	switch s {
	case "llm", "kitchen":
		return s
	}
	return "missing"
}

// Rate is POST /v1/orders/{id}/rating.
func (s *Service) Rate(ctx context.Context, userID, orderID int64, req contracts.RatingRequest) (contracts.Order, error) {
	if req.Rating < 1 || req.Rating > 5 {
		return contracts.Order{}, badRequest("bad_rating", "Nota trebuie să fie între 1 și 5.")
	}
	comment := cut(strings.TrimSpace(req.Comment), 2000)
	o, err := s.Store.Rate(ctx, userID, orderID, req.Rating, comment, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		return contracts.Order{}, notFound("not_found", "Nu am găsit comanda asta.")
	}
	if err != nil {
		return contracts.Order{}, err
	}
	if comment != "" && s.Cfg.LLMEnabled {
		_ = s.publish(ctx, contracts.SubjectFeedbackParseRequested,
			contracts.FeedbackParseRequested{OrderID: o.ID, Rating: req.Rating, Comment: comment})
	}
	return orderView(o), nil
}

// OnFeedbackParsed saves the labels llm found in the comment.
func (s *Service) OnFeedbackParsed(ctx context.Context, ev contracts.FeedbackParsed) error {
	err := s.Store.SetLabels(ctx, ev.OrderID, ev.Labels, cut(strings.TrimSpace(ev.KitchenFeedback), 1000), s.Now())
	if errors.Is(err, store.ErrNotFound) {
		s.Log.Warn("labels for an unknown order", "order", ev.OrderID)
		return nil
	}
	return err
}

// Orders is GET /v1/orders?from=&to= (oldest first).
func (s *Service) Orders(ctx context.Context, userID int64, from, to string) ([]contracts.Order, error) {
	from, to, err := s.period(from, to, 3660)
	if err != nil {
		return nil, err
	}
	orders, err := s.Store.Orders(ctx, userID, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]contracts.Order, 0, len(orders))
	for _, o := range orders {
		out = append(out, orderView(o))
	}
	return out, nil
}

func orderView(o store.Order) contracts.Order {
	v := contracts.Order{
		ID: o.ID, Date: o.Date, DishID: o.DishID, DishName: o.DishName, Price: o.Price, Rating: o.Rating,
		Comment: o.Comment, Forced: o.Forced, PortionG: o.PortionG,
		KcalMin: o.KcalMin, KcalMax: o.KcalMax, ProteinGMin: o.ProteinGMin, ProteinGMax: o.ProteinGMax,
		FatGMin: o.FatGMin, FatGMax: o.FatGMax, CarbsGMin: o.CarbsGMin, CarbsGMax: o.CarbsGMax,
		NutritionSource: o.NutritionSource,
	}
	if o.Labels != "" {
		v.Labels = []byte(o.Labels)
	}
	return v
}

// --------------------------------------------------------------- prefs

// Prefs is GET /v1/prefs.
func (s *Service) Prefs(ctx context.Context, userID int64) (contracts.Prefs, error) {
	ps, err := s.Store.Prefs(ctx, userID)
	if err != nil {
		return contracts.Prefs{}, err
	}
	return contracts.Prefs{Items: prefsView(ps)}, nil
}

// SetPrefs is PUT /v1/prefs: the whole list. An allergen is always a hard filter.
func (s *Service) SetPrefs(ctx context.Context, userID int64, in contracts.Prefs) (contracts.Prefs, error) {
	if len(in.Items) > 200 {
		return contracts.Prefs{}, badRequest("too_many", "Prea multe preferințe (cel mult 200).")
	}
	seen := map[string]bool{}
	var items []store.Pref
	for _, p := range in.Items {
		kind := strings.ToLower(strings.TrimSpace(p.Kind))
		value := strings.Join(strings.Fields(p.Value), " ")
		if !rules.Kinds[kind] {
			return contracts.Prefs{}, badRequest("bad_pref", "Tip de preferință necunoscut: „%s” (allergen, dislike, like, diet, target).", p.Kind)
		}
		if value == "" || len([]rune(value)) > 100 {
			return contracts.Prefs{}, badRequest("bad_pref", "Valoarea unei preferințe trebuie să aibă 1-100 de caractere.")
		}
		if kind == rules.KindTarget {
			name, v, err := rules.ParseTarget(value)
			if err != nil {
				return contracts.Prefs{}, badRequest("bad_pref", "%s", err.Error())
			}
			value = name + "=" + strconv.FormatFloat(v, 'f', -1, 64)
		}
		hard := p.Hard || kind == rules.KindAllergen
		key := kind + "\x00" + strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, store.Pref{Kind: kind, Value: value, Hard: hard})
	}
	if err := s.Store.ReplacePrefs(ctx, userID, items); err != nil {
		return contracts.Prefs{}, err
	}
	return s.Prefs(ctx, userID)
}

// --------------------------------------------------------------- stats

// Stats is GET /v1/stats?from=&to=: everything straight from orders, no
// calls to catalog. Nutrition is summed separately on min and on max, so a
// total is still an interval; orders without values are counted apart.
func (s *Service) Stats(ctx context.Context, userID int64, from, to string) (contracts.Stats, error) {
	from, to, err := s.period(from, to, 3660)
	if err != nil {
		return contracts.Stats{}, err
	}
	orders, err := s.Store.Orders(ctx, userID, from, to)
	if err != nil {
		return contracts.Stats{}, err
	}
	st := contracts.Stats{From: from, To: to, TopDishes: []contracts.DishCount{}, Days: []contracts.DayStats{}, Months: []contracts.MonthStats{}}

	type dishAgg struct {
		name         string
		count, rated int
		sum          float64
	}
	dishes := map[int64]*dishAgg{}
	var dayOrder []string
	daysBy := map[string]*contracts.DayStats{}
	dayRating := map[string][2]float64{}
	var monthOrder []string
	monthsBy := map[string]*contracts.MonthStats{}
	ratingSum := 0.0

	for _, o := range orders {
		st.Orders++
		price := 0.0
		if o.Price != nil {
			price = *o.Price
		}
		st.Spent += price
		if o.Forced {
			st.Forced++
		}
		if o.Rating != nil {
			st.Rated++
			ratingSum += float64(*o.Rating)
		}

		d := daysBy[o.Date]
		if d == nil {
			d = &contracts.DayStats{Date: o.Date}
			daysBy[o.Date] = d
			dayOrder = append(dayOrder, o.Date)
		}
		d.Orders++
		d.Spent += price
		if o.Rating != nil {
			r := dayRating[o.Date]
			dayRating[o.Date] = [2]float64{r[0] + float64(*o.Rating), r[1] + 1}
		}

		mk := o.Date[:7]
		m := monthsBy[mk]
		if m == nil {
			m = &contracts.MonthStats{Month: mk}
			monthsBy[mk] = m
			monthOrder = append(monthOrder, mk)
		}
		m.Orders++
		m.Spent += price

		if o.NutritionSource == "missing" || o.KcalMin == nil && o.ProteinGMin == nil && o.FatGMin == nil && o.CarbsGMin == nil {
			st.WithoutNutrition++
			d.WithoutNutrition++
		} else {
			st.Kcal = addRange(st.Kcal, o.KcalMin, o.KcalMax)
			st.ProteinG = addRange(st.ProteinG, o.ProteinGMin, o.ProteinGMax)
			st.FatG = addRange(st.FatG, o.FatGMin, o.FatGMax)
			st.CarbsG = addRange(st.CarbsG, o.CarbsGMin, o.CarbsGMax)
			d.Kcal = addRange(d.Kcal, o.KcalMin, o.KcalMax)
			d.ProteinG = addRange(d.ProteinG, o.ProteinGMin, o.ProteinGMax)
			d.FatG = addRange(d.FatG, o.FatGMin, o.FatGMax)
			d.CarbsG = addRange(d.CarbsG, o.CarbsGMin, o.CarbsGMax)
		}

		a := dishes[o.DishID]
		if a == nil {
			a = &dishAgg{}
			dishes[o.DishID] = a
		}
		a.name = o.DishName // the newest name wins
		a.count++
		if o.Rating != nil {
			a.rated++
			a.sum += float64(*o.Rating)
		}
	}

	if st.Orders > 0 {
		st.ForcedPercent = round2(100 * float64(st.Forced) / float64(st.Orders))
	}
	if st.Rated > 0 {
		avg := round2(ratingSum / float64(st.Rated))
		st.AvgRating = &avg
	}
	st.Spent = round2(st.Spent)
	for _, k := range dayOrder {
		d := daysBy[k]
		d.Spent = round2(d.Spent)
		if r := dayRating[k]; r[1] > 0 {
			avg := round2(r[0] / r[1])
			d.Rating = &avg
		}
		st.Days = append(st.Days, *d)
	}
	for _, k := range monthOrder {
		m := monthsBy[k]
		m.Spent = round2(m.Spent)
		st.Months = append(st.Months, *m)
	}
	for id, a := range dishes {
		dc := contracts.DishCount{DishID: id, Name: a.name, Count: a.count}
		if a.rated > 0 {
			avg := round2(a.sum / float64(a.rated))
			dc.AvgRating = &avg
		}
		st.TopDishes = append(st.TopDishes, dc)
	}
	sort.Slice(st.TopDishes, func(i, j int) bool {
		a, b := st.TopDishes[i], st.TopDishes[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		ra, rb := 0.0, 0.0
		if a.AvgRating != nil {
			ra = *a.AvgRating
		}
		if b.AvgRating != nil {
			rb = *b.AvgRating
		}
		if ra != rb {
			return ra > rb
		}
		return a.DishID < b.DishID
	})
	if len(st.TopDishes) > 10 {
		st.TopDishes = st.TopDishes[:10]
	}
	return st, nil
}

// addRange adds an interval to a total; a value known only on one side is
// used for both (the snapshot always keeps both, so this is a safety net).
func addRange(total *contracts.Range, lo, hi *float64) *contracts.Range {
	if lo == nil && hi == nil {
		return total
	}
	if lo == nil {
		lo = hi
	}
	if hi == nil {
		hi = lo
	}
	if total == nil {
		total = &contracts.Range{}
	}
	total.Min = round2(total.Min + *lo)
	total.Max = round2(total.Max + *hi)
	return total
}

func round2(f float64) float64 {
	if f < 0 {
		return -round2(-f)
	}
	return float64(int64(f*100+0.5)) / 100
}
