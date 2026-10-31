// Package service is the logic of catalog: it saves the menus the source
// sends, asks llm only for what is missing, saves llm's answers, tells core
// when a requested menu is ready, and answers the HTTP questions about
// dishes. catalog never computes nutrition: it gets it from the source or
// from llm and keeps who gave what.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"food-order/catalog/internal/applog"
	"food-order/catalog/internal/bus"
	"food-order/catalog/internal/config"
	"food-order/catalog/internal/contracts"
	"food-order/catalog/internal/food"
	"food-order/catalog/internal/store"
)

// Error is an error with an HTTP status and a code; Message is in Romanian.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func badRequest(code, msg string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: code, Message: msg}
}

// Service holds everything the logic needs.
type Service struct {
	Store *store.Store
	Bus   bus.Publisher
	Cfg   config.Config
	Log   *slog.Logger
	Now   func() time.Time

	mu          sync.Mutex // one import / flush at a time: nothing is published twice
	lastCleanup time.Time
}

// New makes a Service.
func New(st *store.Store, pub bus.Publisher, cfg config.Config, log *slog.Logger) *Service {
	return &Service{Store: st, Bus: pub, Cfg: cfg, Log: log, Now: time.Now}
}

// Limits of the HTTP API.
const (
	maxIDs        = 500
	embedBatch    = 50
	maxComponents = 60
)

// ---------------------------------------------------------------- menu in

// Import saves a menu from the source (food.menu.fetched, or POST /v1/import),
// remembers the request of core and sends what is missing to llm.
func (s *Service) Import(ctx context.Context, m contracts.MenuFetched) (contracts.ImportResponse, error) {
	now := s.Now()
	source := strings.ToLower(strings.TrimSpace(m.Source))
	if source == "" {
		source = "parser"
	}
	reqID := strings.TrimSpace(m.RequestID)
	if reqID == "" { // a menu nobody asked for (e.g. the api later): core still hears it is ready
		reqID = "catalog-" + bus.NewID(now)
	}

	ctx = applog.With(ctx, "request_id", reqID, "source", source)
	days0 := make([]string, 0, len(m.Days))
	for _, d := range m.Days {
		days0 = append(days0, fmt.Sprintf("%s (%d)", d.Date, len(d.Items)))
	}
	s.Log.InfoContext(ctx, "menu received", "revision", m.Revision, "days", days0)

	type day struct {
		date  string
		rev   int
		items []store.Item
	}
	var days []day
	for _, d := range m.Days {
		date := strings.TrimSpace(d.Date)
		if _, err := time.Parse(store.DateFormat, date); err != nil {
			s.Log.WarnContext(ctx, "menu day with a bad date, skipped", "request_id", reqID, "date", d.Date)
			continue
		}
		var items []store.Item
		for _, si := range d.Items {
			if it, ok := s.toItem(ctx, si); ok {
				items = append(items, it)
			} else {
				s.Log.WarnContext(ctx, "dish without a name, skipped", "date", date, "item", si)
			}
		}
		if len(items) == 0 { // an empty day is "no menu", never "remove the menu"
			s.Log.WarnContext(ctx, "menu day without dishes, skipped", "request_id", reqID, "date", date)
			continue
		}
		rev := d.Revision
		if rev <= 0 {
			rev = m.Revision
		}
		days = append(days, day{date, rev, items})
	}
	if len(days) == 0 {
		s.Log.WarnContext(ctx, "menu refused: no day with dishes", "days", len(m.Days))
		return contracts.ImportResponse{}, badRequest("bad_menu", "Meniul nu are nicio zi cu feluri (date YYYY-MM-DD și feluri cu nume).")
	}
	sort.Slice(days, func(i, j int) bool { return days[i].date < days[j].date })

	out := contracts.ImportResponse{RequestID: reqID}
	analysisOn := s.Cfg.AnalysisEnabled
	s.mu.Lock()
	for _, d := range days {
		res, err := s.Store.ImportDay(ctx, source, d.date, d.rev, d.items, analysisOn, now)
		if err != nil {
			s.mu.Unlock()
			return contracts.ImportResponse{}, fmt.Errorf("import %s: %w", d.date, err)
		}
		s.Log.DebugContext(ctx, "menu day saved", "date", d.date, "revision", res.Revision, "items", res.Items,
			"changed", res.Changed, "skipped", res.Skipped, "analysis", analysisOn)
		if res.Skipped {
			s.Log.WarnContext(ctx, "older menu revision ignored", "date", d.date, "revision", d.rev, "saved", res.Revision)
		}
		out.Days = append(out.Days, contracts.DayImport{Date: res.Date, Revision: res.Revision, Items: res.Items,
			Skipped: res.Skipped, Changed: res.Changed})
	}
	err := s.Store.SaveRequest(ctx, reqID, source, days[0].date, days[len(days)-1].date, now)
	s.mu.Unlock()
	if err != nil {
		return contracts.ImportResponse{}, err
	}
	s.Log.InfoContext(ctx, "menu saved", "request_id", reqID, "source", source, "from", days[0].date, "to", days[len(days)-1].date, "days", len(days))
	if err := s.Flush(ctx); err != nil {
		s.Log.WarnContext(ctx, "flush after import", "err", err)
	}
	return out, nil
}

// toItem cleans one dish from the source; ok = false for a dish without a name.
func (s *Service) toItem(ctx context.Context, si contracts.SourceItem) (store.Item, bool) {
	it := store.Item{
		ExternalID:  strings.TrimSpace(si.ExternalID),
		Name:        strings.Join(strings.Fields(si.Name), " "),
		Category:    strings.TrimSpace(si.Category),
		Description: strings.TrimSpace(si.Description),
		ImageURL:    strings.TrimSpace(si.ImageURL),
		WeightText:  strings.TrimSpace(si.WeightText),
		Allergens:   si.Allergens,
	}
	if it.Name == "" {
		return it, false
	}
	if si.Price != nil && *si.Price >= 0 && !math.IsInf(*si.Price, 0) {
		it.Price = si.Price
	}
	if it.WeightText == "" {
		it.WeightText = strings.TrimSpace(si.Weight)
	}
	if si.WeightG != nil && *si.WeightG > 0 {
		it.WeightG = si.WeightG
	} else {
		it.WeightG = food.ParseWeight(it.WeightText)
	}
	for _, c := range si.Components {
		c.Name = strings.Join(strings.Fields(c.Name), " ")
		if c.Name == "" || len(it.Components) >= maxComponents {
			continue
		}
		c.Method = contracts.NormMethod(c.Method)
		if c.Grams != nil && *c.Grams < 0 {
			c.Grams = nil
		}
		it.Components = append(it.Components, c)
	}
	s.Log.DebugContext(ctx, "dish from the source", "dish", it.Name, "external_id", it.ExternalID, "category", it.Category,
		"price", it.Price, "weight_text", it.WeightText, "weight_g", it.WeightG, "components", len(it.Components),
		"allergens", it.Allergens, "nutrition", rawOrNil(si.Nutrition))
	n, per100, err := food.ParseNutrition(si.Nutrition)
	if err != nil {
		s.Log.WarnContext(ctx, "source nutrition not understood, ignored", "dish", it.Name, "err", err)
	}
	if n != nil {
		if raw, err := json.Marshal(json.RawMessage(si.Nutrition)); err == nil {
			it.NutritionRaw = string(raw)
		}
		switch {
		case !per100:
			it.Nutrition = n
		case it.WeightG != nil: // "per 100 g": the portion is weight_g / 100 of it
			it.Nutrition = n.Scale(*it.WeightG / 100)
		default: // per 100 g, but how big is a portion? llm estimates it, then it is scaled
			it.Per100 = true
		}
	}
	return it, true
}

func rawOrNil(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// ------------------------------------------------------------ llm answers

// OnAnalyzed saves llm's analysis of a dish (food.dish.analyzed).
func (s *Service) OnAnalyzed(ctx context.Context, a contracts.DishAnalyzed) error {
	s.mu.Lock()
	found, err := s.Store.ApplyAnalysis(ctx, a, s.Now())
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if !found {
		s.Log.InfoContext(ctx, "analysis for a menu item that is gone (newer menu revision), ignored", "menu_item_id", a.MenuItemID)
		return nil
	}
	s.Log.InfoContext(ctx, "dish analysed", "menu_item_id", a.MenuItemID, "model", a.Model)
	s.Log.DebugContext(ctx, "analysis", "analysis", a)
	if err := s.Flush(ctx); err != nil {
		s.Log.WarnContext(ctx, "flush after analysis", "err", err)
	}
	return nil
}

// OnEmbedded saves the embedding of a dish (food.dish.embedded).
func (s *Service) OnEmbedded(ctx context.Context, e contracts.DishEmbedded) error {
	if len(e.Vector) == 0 {
		return &Error{Status: http.StatusUnprocessableEntity, Code: "bad_vector", Message: "vectorul e gol"}
	}
	for _, f := range e.Vector {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return &Error{Status: http.StatusUnprocessableEntity, Code: "bad_vector", Message: "vectorul are valori nevalide"}
		}
	}
	err := s.Store.SaveEmbedding(ctx, e.DishID, e.Model, e.Vector, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		s.Log.WarnContext(ctx, "embedding for an unknown dish, ignored", "dish_id", e.DishID)
		return nil
	}
	if err == nil {
		s.Log.InfoContext(ctx, "embedding saved", "dish_id", e.DishID, "model", e.Model, "dims", len(e.Vector))
	}
	return err
}

// ------------------------------------------------------------------ flush

// Flush does everything that is due: gives up on llm answers that are too
// late, sends the analysis and embedding requests that are not out yet, and
// publishes food.menu.ready for the requests that are complete. It runs
// after every event and at every tick, so a publish that failed (broker
// down) is simply tried again.
func (s *Service) Flush(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	start := now
	var errs []error
	defer func() {
		s.Log.DebugContext(ctx, "flush done", "ms", applog.Since(start), "errors", len(errs))
	}()

	if s.Cfg.AnalysisEnabled {
		late, err := s.Store.TimeoutAnalyses(ctx, now.Add(-s.Cfg.AnalysisTimeout))
		if err != nil {
			errs = append(errs, err)
		}
		if len(late) > 0 {
			names := make([]string, 0, len(late))
			for i, t := range late {
				if i == 10 {
					names = append(names, fmt.Sprintf("încă %d", len(late)-10))
					break
				}
				names = append(names, t.Name+" ("+t.Date+")")
			}
			s.Log.WarnContext(ctx, "llm did not answer in time", "items", len(late), "timeout", s.Cfg.AnalysisTimeout)
			s.alert(ctx, "warn", fmt.Sprintf("llm nu a analizat %d feluri în %s: %s. Meniul merge cu ce există (nutriție lipsă).",
				len(late), s.Cfg.AnalysisTimeout, strings.Join(names, ", ")))
		}
	}

	jobs, err := s.Store.PendingAnalyses(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	if len(jobs) > 0 {
		s.Log.DebugContext(ctx, "analyses to request", "count", len(jobs))
	}
	for _, j := range jobs {
		if err := s.Bus.Publish(ctx, contracts.SubjectDishAnalyzeRequested, j.Request); err != nil {
			errs = append(errs, s.publishErr(err))
			break // the broker is down: the rest waits for the next tick
		}
		if err := s.Store.MarkRequested(ctx, j.Items, now); err != nil {
			errs = append(errs, err)
		}
		s.Log.InfoContext(ctx, "analysis requested", "dish_id", j.Request.DishID, "menu_item_id", j.Request.MenuItemID, "need", j.Request.Need,
			"items", len(j.Items))
	}

	if s.Cfg.EmbedEnabled {
		embeds, err := s.Store.PendingEmbeds(ctx, embedBatch)
		if err != nil {
			errs = append(errs, err)
		}
		for _, e := range embeds {
			if err := s.Bus.Publish(ctx, contracts.SubjectDishEmbedRequested, e); err != nil {
				errs = append(errs, s.publishErr(err))
				break
			}
			if err := s.Store.MarkEmbedRequested(ctx, e.DishID, now); err != nil {
				errs = append(errs, err)
			}
			s.Log.DebugContext(ctx, "embedding requested", "dish_id", e.DishID, "text", e.Text)
		}
	}

	if err := s.Store.CompleteRequests(ctx, now); err != nil {
		errs = append(errs, err)
	}
	ready, err := s.Store.ReadyToPublish(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	for _, r := range ready {
		if err := s.Bus.Publish(ctx, contracts.SubjectMenuReady, r); err != nil {
			errs = append(errs, s.publishErr(err))
			break
		}
		if err := s.Store.MarkPublished(ctx, r.RequestID, now); err != nil {
			errs = append(errs, err)
		}
		s.Log.InfoContext(ctx, "menu ready", "request_id", r.RequestID, "from", r.From, "to", r.To, "dishes", len(r.DishIDs))
	}
	return errors.Join(errs...)
}

// publishErr hides ErrOff (running without a broker is not an error).
func (s *Service) publishErr(err error) error {
	if errors.Is(err, bus.ErrOff) {
		return nil
	}
	return err
}

func (s *Service) alert(ctx context.Context, level, text string) {
	if level == "error" {
		s.Log.ErrorContext(ctx, "alert to the admins", "alert_level", level, "text", text)
	} else {
		s.Log.WarnContext(ctx, "alert to the admins", "alert_level", level, "text", text)
	}
	if err := s.Bus.Publish(ctx, contracts.SubjectAlert, contracts.Alert{Service: "catalog", Level: level, Text: text}); err != nil &&
		!errors.Is(err, bus.ErrOff) {
		s.Log.WarnContext(ctx, "alert not sent", "err", err, "text", text)
	}
}

// Tick runs Flush and, once an hour, forgets old processed event ids.
func (s *Service) Tick(ctx context.Context) {
	ctx = applog.With(ctx, "job", "tick", "tick_id", bus.NewID(s.Now()))
	if err := s.Flush(ctx); err != nil {
		s.Log.WarnContext(ctx, "tick", "err", err)
	}
	now := s.Now()
	if now.Sub(s.lastCleanup) < time.Hour {
		return
	}
	s.lastCleanup = now
	s.Log.DebugContext(ctx, "forgetting processed event ids", "before", now.Add(-s.Cfg.ProcessedKeep))
	if err := s.Store.ForgetProcessed(ctx, now.Add(-s.Cfg.ProcessedKeep)); err != nil {
		s.Log.WarnContext(ctx, "forget processed events", "err", err)
	}
}

// ------------------------------------------------------------- HTTP reads

// Menu returns the dishes of a day; without a date, tomorrow.
func (s *Service) Menu(ctx context.Context, date string) (contracts.MenuResponse, error) {
	if date == "" {
		date = s.Now().In(s.Cfg.Location).AddDate(0, 0, 1).Format(store.DateFormat)
	}
	if err := checkDate(date); err != nil {
		return contracts.MenuResponse{}, err
	}
	items, err := s.Store.Menu(ctx, date)
	if err != nil {
		return contracts.MenuResponse{}, err
	}
	s.Log.DebugContext(ctx, "menu read", "date", date, "items", len(items))
	return contracts.MenuResponse{Date: date, Items: items}, nil
}

// Dishes returns dishes by id ("12,15,18"), with the values of the menu of
// date (or of the latest day they were on the menu).
func (s *Service) Dishes(ctx context.Context, ids, date string) (contracts.DishesResponse, error) {
	list, err := parseIDs(ids)
	if err != nil {
		return contracts.DishesResponse{}, err
	}
	if date != "" {
		if err := checkDate(date); err != nil {
			return contracts.DishesResponse{}, err
		}
	}
	items, err := s.Store.DishesByIDs(ctx, list, date)
	if err != nil {
		return contracts.DishesResponse{}, err
	}
	s.Log.DebugContext(ctx, "dishes read", "ids", list, "date", date, "found", len(items))
	return contracts.DishesResponse{Items: items}, nil
}

// Dish returns one dish; 404 when it is unknown or not on that day's menu.
func (s *Service) Dish(ctx context.Context, id int64, date string) (contracts.Dish, error) {
	if date != "" {
		if err := checkDate(date); err != nil {
			return contracts.Dish{}, err
		}
	}
	items, err := s.Store.DishesByIDs(ctx, []int64{id}, date)
	if err != nil {
		return contracts.Dish{}, err
	}
	if len(items) == 0 {
		s.Log.DebugContext(ctx, "dish not found", "dish_id", id, "date", date)
		msg := "Nu există felul acesta."
		if date != "" {
			msg = "Felul nu e în meniul din " + date + "."
		}
		return contracts.Dish{}, &Error{Status: http.StatusNotFound, Code: "not_found", Message: msg}
	}
	return items[0], nil
}

// Similar says how much each candidate looks like the liked dishes: the
// best cosine similarity of the embeddings (same model), from 0 to 1. A
// dish is not compared with itself; candidates without an embedding are
// left out.
func (s *Service) Similar(ctx context.Context, req contracts.SimilarRequest) (contracts.SimilarResponse, error) {
	out := contracts.SimilarResponse{Scores: []contracts.SimilarScore{}}
	if len(req.LikedIDs) > maxIDs || len(req.CandidateIDs) > maxIDs {
		return out, badRequest("too_many_ids", fmt.Sprintf("Cel mult %d id-uri în fiecare listă.", maxIDs))
	}
	if !s.Cfg.EmbedEnabled || len(req.LikedIDs) == 0 || len(req.CandidateIDs) == 0 {
		s.Log.DebugContext(ctx, "similarity skipped", "embed_enabled", s.Cfg.EmbedEnabled, "liked", len(req.LikedIDs), "candidates", len(req.CandidateIDs))
		return out, nil
	}
	all := append(append([]int64{}, req.LikedIDs...), req.CandidateIDs...)
	emb, err := s.Store.Embeddings(ctx, all)
	if err != nil {
		return out, err
	}
	seen := map[int64]bool{}
	for _, c := range req.CandidateIDs {
		ec, ok := emb[c]
		if !ok || seen[c] {
			continue
		}
		seen[c] = true
		best, bestID := -2.0, int64(0)
		for _, l := range req.LikedIDs {
			el, ok := emb[l]
			if !ok || l == c || el.Model != ec.Model || len(el.Vector) != len(ec.Vector) {
				continue
			}
			if v := cosine(ec.Vector, el.Vector); v > best {
				best, bestID = v, l
			}
		}
		if bestID == 0 {
			continue
		}
		out.Scores = append(out.Scores, contracts.SimilarScore{ID: c, Score: math.Max(0, math.Min(1, best)), LikeID: bestID})
	}
	s.Log.DebugContext(ctx, "similarity computed", "liked", len(req.LikedIDs), "candidates", len(req.CandidateIDs),
		"with_embedding", len(emb), "scores", out.Scores)
	return out, nil
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ConfirmAllergens saves a user's confirmation of the allergens of a dish
// and returns the dish as it is now.
func (s *Service) ConfirmAllergens(ctx context.Context, userID, dishID int64, allergens []string) (contracts.Dish, error) {
	if allergens == nil {
		return contracts.Dish{}, badRequest("bad_allergens", "Lipsește lista allergens (o listă goală = fără alergeni).")
	}
	err := s.Store.ConfirmAllergens(ctx, dishID, food.NormList(allergens), userID, s.Now())
	if errors.Is(err, store.ErrNotFound) {
		s.Log.InfoContext(ctx, "allergens for an unknown dish", "dish_id", dishID, "user_id", userID)
		return contracts.Dish{}, &Error{Status: http.StatusNotFound, Code: "not_found", Message: "Nu există felul acesta."}
	}
	if err != nil {
		return contracts.Dish{}, err
	}
	s.Log.InfoContext(ctx, "allergens confirmed", "dish_id", dishID, "user_id", userID, "allergens", food.NormList(allergens))
	return s.Dish(ctx, dishID, "")
}

func checkDate(date string) error {
	if _, err := time.Parse(store.DateFormat, date); err != nil {
		return badRequest("bad_date", "Data trebuie să fie de forma AAAA-LL-ZZ (de ex. 2026-11-02).")
	}
	return nil
}

func parseIDs(s string) ([]int64, error) {
	var out []int64
	seen := map[int64]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil || id <= 0 {
			return nil, badRequest("bad_ids", "ids trebuie să fie numere separate prin virgulă (de ex. 12,15,18).")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, badRequest("bad_ids", "Lipsește ids (de ex. ?ids=12,15,18).")
	}
	if len(out) > maxIDs {
		return nil, badRequest("too_many_ids", fmt.Sprintf("Cel mult %d id-uri.", maxIDs))
	}
	return out, nil
}
