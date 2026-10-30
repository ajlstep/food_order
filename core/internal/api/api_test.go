package api_test

// End-to-end tests of core without NATS, catalog or Telegram: a real
// SQLite file in a temp dir, a fake catalog, a publisher that records the
// events, the events handler fed by hand and a clock the test moves.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"food-order/core/internal/api"
	"food-order/core/internal/bus"
	"food-order/core/internal/catalog"
	"food-order/core/internal/config"
	"food-order/core/internal/contracts"
	"food-order/core/internal/events"
	"food-order/core/internal/service"
	"food-order/core/internal/store"
)

// ---------------------------------------------------------------- fakes

type fakeCatalog struct {
	mu   sync.Mutex
	menu map[string][]contracts.CatalogDish
	down bool
}

func (c *fakeCatalog) Menu(_ context.Context, date string) ([]contracts.CatalogDish, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return nil, errors.New("catalog: connection refused")
	}
	return c.menu[date], nil
}

func (c *fakeCatalog) Dishes(ctx context.Context, ids []int64, date string) ([]contracts.CatalogDish, error) {
	all, err := c.Menu(ctx, date)
	if err != nil {
		return nil, err
	}
	var out []contracts.CatalogDish
	for _, d := range all {
		for _, id := range ids {
			if d.ID == id {
				out = append(out, d)
			}
		}
	}
	if len(out) == 0 {
		return nil, catalog.ErrNotFound
	}
	return out, nil
}

func (c *fakeCatalog) Similar(context.Context, []int64, []int64) (map[int64]float64, error) {
	return nil, errors.New("not here")
}

type sent struct {
	subject string
	raw     []byte
}

type fakePub struct {
	mu   sync.Mutex
	evs  []sent
	fail bool
}

func (p *fakePub) Publish(_ context.Context, subject string, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return errors.New("nats: no servers available")
	}
	raw, _ := json.Marshal(data)
	p.evs = append(p.evs, sent{subject, raw})
	return nil
}

func (p *fakePub) all(subject string) [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out [][]byte
	for _, e := range p.evs {
		if e.subject == subject {
			out = append(out, e.raw)
		}
	}
	return out
}

func (p *fakePub) last(t *testing.T, subject string, out any) {
	t.Helper()
	all := p.all(subject)
	if len(all) == 0 {
		t.Fatalf("no %s published", subject)
	}
	if err := json.Unmarshal(all[len(all)-1], out); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------- env

type env struct {
	t   *testing.T
	svc *service.Service
	h   http.Handler
	ev  *events.Handler
	pub *fakePub
	cat *fakeCatalog
	now time.Time
	mu  sync.Mutex
}

var loc, _ = time.LoadLocation("Europe/Chisinau")

func newEnv(t *testing.T, mod ...func(*config.Config)) *env {
	t.Helper()
	cfg := config.Defaults()
	for _, m := range mod {
		m(&cfg)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, pub: &fakePub{}, cat: &fakeCatalog{menu: map[string][]contracts.CatalogDish{}},
		now: time.Date(2026, 10, 30, 12, 0, 0, 0, loc)} // Friday
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.svc = service.New(st, e.cat, e.pub, cfg, log)
	e.svc.Now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
	e.h = api.New(e.svc, st, log)
	e.ev = events.New(e.svc)
	return e
}

func (e *env) at(t time.Time)          { e.mu.Lock(); e.now = t; e.mu.Unlock() }
func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }
func (e *env) tick()                   { e.svc.Tick(context.Background()) }

func (e *env) call(method, path string, user int64, body any) (int, []byte) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if user > 0 {
		req.Header.Set(contracts.HeaderUserID, strconv.FormatInt(user, 10))
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func (e *env) callOK(method, path string, user int64, body, out any) {
	e.t.Helper()
	code, raw := e.call(method, path, user, body)
	if code >= 300 {
		e.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) deliver(subject string, data any) []byte {
	e.t.Helper()
	envl, err := bus.NewEnvelope(subject, data, e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	raw, _ := json.Marshal(envl)
	if err := e.ev.Handle(context.Background(), subject, raw); err != nil {
		e.t.Fatalf("handle %s: %v", subject, err)
	}
	return raw
}

func errCode(raw []byte) string {
	var eb contracts.ErrorBody
	_ = json.Unmarshal(raw, &eb)
	return eb.Error.Code
}

func fp(v float64) *float64 { return &v }

func llmNutrition(lo, hi float64) *contracts.CatalogNutrition {
	return &contracts.CatalogNutrition{Kcal: &contracts.Range{Min: lo, Max: hi}, ProteinG: &contracts.Range{Min: 20, Max: 30}}
}

func menu() []contracts.CatalogDish {
	return []contracts.CatalogDish{
		{ID: 1, Name: "Zeamă de găină", Price: fp(45), NutritionSource: "llm", Nutrition: llmNutrition(300, 400)},
		{ID: 2, Name: "Paste cu brânză", Price: fp(60), Components: []contracts.CatalogComponent{{Name: "brânză"}}},
		{ID: 3, Name: "Pilaf", Price: fp(55), Allergens: contracts.CatalogAllergens{Possible: []string{"gluten"}}},
		{ID: 4, Name: "Sarmale", Price: fp(63), NutritionSource: "llm", Nutrition: llmNutrition(600, 800)},
	}
}

// ---------------------------------------------------------------- tests

func TestNeedsUser(t *testing.T) {
	e := newEnv(t)
	code, raw := e.call("GET", "/v1/prefs", 0, nil)
	if code != 400 || errCode(raw) != "missing_user" {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _ := e.call("GET", "/healthz", 0, nil); code != 200 {
		t.Fatalf("healthz %d", code)
	}
}

func TestRecommendationFlow(t *testing.T) {
	e := newEnv(t)
	day := "2026-11-02"
	e.cat.menu[day] = menu()

	var prefs contracts.Prefs
	e.callOK("PUT", "/v1/prefs", 7, contracts.Prefs{Items: []contracts.Pref{{Kind: "allergen", Value: "lapte"}, {Kind: "like", Value: "sarmale"}}}, &prefs)
	if len(prefs.Items) != 2 || !prefs.Items[0].Hard {
		t.Fatalf("prefs: %+v", prefs)
	}

	// no menu yet: the recommendation waits and core asks for the menu
	var rec contracts.Recommendation
	code, raw := e.call("POST", "/v1/recommendations", 7, contracts.CreateRecommendationRequest{Date: day})
	if code != http.StatusAccepted {
		t.Fatalf("%d %s", code, raw)
	}
	_ = json.Unmarshal(raw, &rec)
	if rec.Status != "waiting_menu" {
		t.Fatalf("status %q", rec.Status)
	}
	var mreq contracts.MenuRequested
	e.pub.last(t, contracts.SubjectMenuRequested, &mreq)
	if mreq.From != day || mreq.RequestID == "" {
		t.Fatalf("menu request %+v", mreq)
	}
	// asking again (bot scheduler + /meniu) does not double anything
	e.callOK("POST", "/v1/recommendations", 7, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != 1 {
		t.Fatalf("menu requested %d times", n)
	}

	// the menu is in catalog -> scored, top goes to llm
	e.deliver(contracts.SubjectMenuReady, contracts.MenuReady{RequestID: mreq.RequestID, From: day, To: day, DishIDs: []int64{1, 2, 3, 4}})
	var llmReq contracts.RecommendationRequested
	e.pub.last(t, contracts.SubjectRecommendationRequested, &llmReq)
	got := map[int64]bool{}
	for _, c := range llmReq.Candidates {
		got[c.ID] = true
	}
	if got[2] || !got[1] || !got[3] || !got[4] || llmReq.Candidates[0].ID != 4 {
		t.Fatalf("candidates (milk out, liked first): %+v", llmReq.Candidates)
	}
	if llmReq.Context.MaxPicks != 5 || llmReq.Candidates[0].KcalMin == nil || *llmReq.Candidates[0].KcalMin != 600 {
		t.Fatalf("llm request: %+v", llmReq)
	}
	e.callOK("GET", "/v1/recommendations?date="+day, 7, nil, &rec)
	if rec.Status != "generating" {
		t.Fatalf("status %q", rec.Status)
	}

	// llm answers; an id that was not in the list and a duplicate are dropped
	raw = e.deliver(contracts.SubjectRecommendationGenerated, contracts.RecommendationGenerated{RequestID: llmReq.RequestID,
		Picks: []contracts.GeneratedPick{{ID: 999, Reason: "invented"}, {ID: 4, Reason: "Îți plac sarmalele"}, {ID: 1, Reason: "ceva cald"}, {ID: 4, Reason: "dup"}}})
	var ready contracts.RecommendationReady
	e.pub.last(t, contracts.SubjectRecommendationReady, &ready)
	if ready.UserID != 7 || ready.Date != day || len(ready.Picks) != 2 || ready.Picks[0].DishID != 4 ||
		ready.Picks[0].Reason != "Îți plac sarmalele" || ready.Picks[1].Name != "Zeamă de găină" || *ready.Picks[1].KcalMax != 400 {
		t.Fatalf("ready: %+v", ready)
	}
	// the same event again (redelivery) does nothing
	if err := e.ev.Handle(context.Background(), contracts.SubjectRecommendationGenerated, raw); err != nil {
		t.Fatal(err)
	}
	if n := len(e.pub.all(contracts.SubjectRecommendationReady)); n != 1 {
		t.Fatalf("ready published %d times", n)
	}

	e.callOK("GET", "/v1/recommendations?date="+day, 7, nil, &rec)
	if rec.Status != "ready" || len(rec.Picks) != 2 {
		t.Fatalf("rec: %+v", rec)
	}
	if code, raw := e.call("GET", "/v1/recommendations?date="+day, 8, nil); code != 404 {
		t.Fatalf("other user: %d %s", code, raw)
	}

	// a menu that is already ready: the next user gets it at once (possible allergens shown)
	e.callOK("POST", "/v1/recommendations", 8, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if rec.Status != "generating" {
		t.Fatalf("second user: %+v", rec)
	}
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != 1 {
		t.Fatalf("menu asked again: %d", n)
	}

	// bad input
	if code, raw := e.call("POST", "/v1/recommendations", 7, contracts.CreateRecommendationRequest{Date: "2026-10-01"}); code != 400 || errCode(raw) != "past_date" {
		t.Fatalf("past: %d %s", code, raw)
	}
	if code, raw := e.call("POST", "/v1/recommendations", 7, map[string]string{"date": "mâine"}); code != 400 || errCode(raw) != "bad_date" {
		t.Fatalf("bad date: %d %s", code, raw)
	}
}

func TestPossibleAllergensReachTheBot(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LLMEnabled = false })
	day := "2026-11-02"
	e.cat.menu[day] = menu()
	e.deliver(contracts.SubjectMenuReady, contracts.MenuReady{RequestID: "pushed", From: day, To: day})
	var rec contracts.Recommendation
	e.callOK("POST", "/v1/recommendations", 3, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if rec.Status != "ready" || len(rec.Picks) != 4 {
		t.Fatalf("without llm, picks from the score: %+v", rec)
	}
	for _, p := range rec.Picks {
		if p.DishID == 3 && (len(p.AllergensPossible) != 1 || p.AllergensPossible[0] != "gluten") {
			t.Fatalf("pilaf: %+v", p)
		}
		if p.Reason != "" {
			t.Fatalf("no reason without llm: %+v", p)
		}
	}
	if len(e.pub.all(contracts.SubjectRecommendationRequested)) != 0 {
		t.Fatal("llm must not be asked")
	}
}

func TestOrdersRatingStats(t *testing.T) {
	e := newEnv(t)
	day := "2026-10-31"
	e.cat.menu[day] = menu()

	var o contracts.Order
	e.callOK("POST", "/v1/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 4}, &o)
	if o.ID == 0 || o.DishName != "Sarmale" || *o.Price != 63 || *o.KcalMin != 600 || *o.ProteinGMax != 30 || o.NutritionSource != "llm" {
		t.Fatalf("order: %+v", o)
	}
	// changed my mind: same order, other dish, price from the bot wins
	var o2 contracts.Order
	e.callOK("POST", "/v1/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 3, Price: fp(50), Forced: true}, &o2)
	if o2.ID != o.ID || o2.DishName != "Pilaf" || *o2.Price != 50 || !o2.Forced || o2.NutritionSource != "missing" || o2.KcalMin != nil {
		t.Fatalf("changed: %+v", o2)
	}

	var rated contracts.Order
	e.callOK("POST", "/v1/orders/"+strconv.FormatInt(o.ID, 10)+"/rating", 7, contracts.RatingRequest{Rating: 2, Comment: " prea sărat "}, &rated)
	if *rated.Rating != 2 || rated.Comment != "prea sărat" {
		t.Fatalf("rated: %+v", rated)
	}
	var fpr contracts.FeedbackParseRequested
	e.pub.last(t, contracts.SubjectFeedbackParseRequested, &fpr)
	if fpr.OrderID != o.ID || fpr.Comment != "prea sărat" || fpr.Rating != 2 {
		t.Fatalf("feedback request: %+v", fpr)
	}
	e.deliver(contracts.SubjectFeedbackParsed, contracts.FeedbackParsed{OrderID: o.ID, Labels: json.RawMessage(`{"sărat":"prea mult"}`), KitchenFeedback: "Prea sărat."})
	var list []contracts.Order
	e.callOK("GET", "/v1/orders?from=2026-10-01&to=2026-11-07", 7, nil, &list)
	if len(list) != 1 || !strings.Contains(string(list[0].Labels), "prea mult") {
		t.Fatalf("list: %+v", list)
	}

	if code, raw := e.call("POST", "/v1/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 1}); code != 409 || errCode(raw) != "already_rated" {
		t.Fatalf("after rating: %d %s", code, raw)
	}
	if code, raw := e.call("POST", "/v1/orders/"+strconv.FormatInt(o.ID, 10)+"/rating", 7, contracts.RatingRequest{Rating: 7}); code != 400 || errCode(raw) != "bad_rating" {
		t.Fatalf("bad rating: %d %s", code, raw)
	}
	if code, _ := e.call("POST", "/v1/orders/"+strconv.FormatInt(o.ID, 10)+"/rating", 8, contracts.RatingRequest{Rating: 5}); code != 404 {
		t.Fatalf("other user's order: %d", code)
	}
	if code, raw := e.call("POST", "/v1/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 77}); code != 422 || errCode(raw) != "dish_not_in_menu" {
		t.Fatalf("unknown dish: %d %s", code, raw)
	}
	e.cat.down = true
	if code, raw := e.call("POST", "/v1/orders", 9, contracts.CreateOrderRequest{Date: day, DishID: 4}); code != 503 || errCode(raw) != "catalog_unavailable" {
		t.Fatalf("catalog down: %d %s", code, raw)
	}
	e.cat.down = false

	// stats: min and max summed apart, the order without values counted apart
	e.callOK("POST", "/v1/orders", 9, contracts.CreateOrderRequest{Date: "2026-10-31", DishID: 4}, nil)
	e.cat.menu["2026-11-02"] = menu()
	e.callOK("POST", "/v1/orders", 9, contracts.CreateOrderRequest{Date: "2026-11-02", DishID: 1}, nil)
	e.cat.menu["2026-11-03"] = menu()
	e.callOK("POST", "/v1/orders", 9, contracts.CreateOrderRequest{Date: "2026-11-03", DishID: 3, Forced: true}, nil)
	var st contracts.Stats
	e.callOK("GET", "/v1/stats?from=2026-10-01&to=2026-11-30", 9, nil, &st)
	if st.Orders != 3 || st.Spent != 163 || st.Forced != 1 || st.WithoutNutrition != 1 ||
		st.Kcal == nil || st.Kcal.Min != 900 || st.Kcal.Max != 1200 || len(st.Days) != 3 || len(st.Months) != 2 || len(st.TopDishes) != 3 {
		t.Fatalf("stats: %+v", st)
	}
	if code, raw := e.call("GET", "/v1/stats?from=2026-11-30&to=2026-11-01", 9, nil); code != 400 || errCode(raw) != "bad_period" {
		t.Fatalf("bad period: %d %s", code, raw)
	}
}

func TestOrderSnapshotWhenCatalogIsDown(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LLMEnabled = false })
	day := "2026-11-02"
	e.cat.menu[day] = menu()
	e.deliver(contracts.SubjectMenuReady, contracts.MenuReady{RequestID: "m", From: day, To: day})
	e.callOK("POST", "/v1/recommendations", 5, contracts.CreateRecommendationRequest{Date: day}, nil)
	e.cat.down = true
	var o contracts.Order
	e.callOK("POST", "/v1/orders", 5, contracts.CreateOrderRequest{Date: day, DishID: 1}, &o)
	if o.DishName != "Zeamă de găină" || *o.KcalMax != 400 || *o.Price != 45 {
		t.Fatalf("snapshot from the saved candidates: %+v", o)
	}
}

func TestPrefsValidation(t *testing.T) {
	e := newEnv(t)
	if code, raw := e.call("PUT", "/v1/prefs", 1, contracts.Prefs{Items: []contracts.Pref{{Kind: "hate", Value: "x"}}}); code != 400 || errCode(raw) != "bad_pref" {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _ := e.call("PUT", "/v1/prefs", 1, contracts.Prefs{Items: []contracts.Pref{{Kind: "target", Value: "kcal=lots"}}}); code != 400 {
		t.Fatalf("bad target %d", code)
	}
	var p contracts.Prefs
	e.callOK("PUT", "/v1/prefs", 1, contracts.Prefs{Items: []contracts.Pref{
		{Kind: "target", Value: "kcal 700"}, {Kind: "dislike", Value: "  ciuperci "}, {Kind: "dislike", Value: "Ciuperci"},
	}}, &p)
	if len(p.Items) != 2 || p.Items[0].Value != "kcal=700" || p.Items[1].Value != "ciuperci" {
		t.Fatalf("prefs: %+v", p)
	}
	e.callOK("GET", "/v1/prefs", 2, nil, &p)
	if p.Items == nil || len(p.Items) != 0 {
		t.Fatalf("new user: %+v", p)
	}
}

func TestMenuScheduleAndRetries(t *testing.T) {
	e := newEnv(t)
	e.at(time.Date(2026, 10, 30, 14, 0, 0, 0, loc)) // Friday, before 15:00
	e.tick()
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != 0 {
		t.Fatalf("too early: %d", n)
	}
	e.at(time.Date(2026, 10, 30, 15, 1, 0, 0, loc))
	e.tick()
	e.tick()
	reqs := e.pub.all(contracts.SubjectMenuRequested)
	var mreq contracts.MenuRequested
	_ = json.Unmarshal(reqs[0], &mreq)
	if len(reqs) != 1 || mreq.From != "2026-11-02" {
		t.Fatalf("Friday asks for Monday once: %d %+v", len(reqs), mreq)
	}

	// no_menu: retried after the interval, with the same request_id
	e.deliver(contracts.SubjectMenuError, contracts.MenuError{RequestID: mreq.RequestID, Code: "no_menu"})
	e.advance(20 * time.Minute)
	e.tick()
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != 1 {
		t.Fatalf("retried too early: %d", n)
	}
	e.advance(11 * time.Minute)
	e.tick()
	reqs = e.pub.all(contracts.SubjectMenuRequested)
	var again contracts.MenuRequested
	_ = json.Unmarshal(reqs[len(reqs)-1], &again)
	if len(reqs) != 2 || again.RequestID != mreq.RequestID {
		t.Fatalf("retry: %d %+v", len(reqs), again)
	}

	// past the deadline (Sunday 20:00 for Monday): alert and stop
	e.at(time.Date(2026, 11, 1, 19, 50, 0, 0, loc))
	e.deliver(contracts.SubjectMenuError, contracts.MenuError{RequestID: mreq.RequestID, Code: "no_menu"})
	var alert contracts.Alert
	e.pub.last(t, contracts.SubjectAlert, &alert)
	if alert.Service != "core" || !strings.Contains(alert.Text, "2026-11-02") {
		t.Fatalf("alert: %+v", alert)
	}
	e.advance(2 * time.Hour)
	e.tick()
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != 2 {
		t.Fatalf("asked after the deadline: %d", n)
	}
	// ...but a user asking for a recommendation asks again
	e.callOK("POST", "/v1/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-02"}, nil)
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != 3 {
		t.Fatalf("on demand: %d", n)
	}
}

func TestMenuParseFailedAndTimeouts(t *testing.T) {
	e := newEnv(t)
	e.callOK("POST", "/v1/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-03"}, nil)
	var mreq contracts.MenuRequested
	e.pub.last(t, contracts.SubjectMenuRequested, &mreq)
	e.deliver(contracts.SubjectMenuError, contracts.MenuError{RequestID: mreq.RequestID, Code: "parse_failed", Message: "no cards"})
	if n := len(e.pub.all(contracts.SubjectAlert)); n != 1 {
		t.Fatalf("parse_failed alerts at once: %d", n)
	}
	before := len(e.pub.all(contracts.SubjectMenuRequested))
	e.advance(time.Hour)
	e.tick()
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != before {
		t.Fatal("parse_failed must not be retried")
	}

	// but the user asks again (/meniu) while the old recommendation still
	// waits for the menu: the menu is asked again, once
	var rec contracts.Recommendation
	e.callOK("POST", "/v1/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-03"}, &rec)
	if rec.Status != "waiting_menu" {
		t.Fatalf("status %q", rec.Status)
	}
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != before+1 {
		t.Fatalf("/meniu after parse_failed must ask for the menu again: %d sends, want %d", n, before+1)
	}
	var again contracts.MenuRequested
	e.pub.last(t, contracts.SubjectMenuRequested, &again)
	if again.RequestID == mreq.RequestID || again.From != "2026-11-03" {
		t.Errorf("a new request for the day is expected: %+v (old %s)", again, mreq.RequestID)
	}
	// asked once more while that request is still out: nothing new
	e.callOK("POST", "/v1/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-03"}, nil)
	if n := len(e.pub.all(contracts.SubjectMenuRequested)); n != before+1 {
		t.Fatalf("a request in flight must not be doubled: %d sends", n)
	}
	// the menu comes: the waiting recommendation is generated
	e.cat.menu["2026-11-03"] = menu()
	e.deliver(contracts.SubjectMenuReady, contracts.MenuReady{RequestID: again.RequestID, From: "2026-11-03", To: "2026-11-03"})
	e.callOK("GET", "/v1/recommendations?date=2026-11-03", 1, nil, &rec)
	if rec.Status == "waiting_menu" {
		t.Errorf("still waiting after the menu came: %+v", rec)
	}

	// nobody answers: expired -> retried with a growing pause -> one alert after 5 sends
	e2 := newEnv(t)
	e2.callOK("POST", "/v1/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-04"}, nil)
	for i := 0; i < 30; i++ {
		e2.advance(11 * time.Minute)
		e2.tick()
	}
	sends := len(e2.pub.all(contracts.SubjectMenuRequested))
	var alerts [][]byte // (the daily request for Monday also starts on the way; look only at ours)
	for _, a := range e2.pub.all(contracts.SubjectAlert) {
		if strings.Contains(string(a), "2026-11-04") {
			alerts = append(alerts, a)
		}
	}
	if sends < 6 || len(alerts) != 1 || !strings.Contains(string(alerts[0]), "nu răspunde") {
		t.Fatalf("sends %d alerts %d %s", sends, len(alerts), alerts)
	}
}

func TestLLMTimeoutAndBrokerDown(t *testing.T) {
	e := newEnv(t)
	day := "2026-11-02"
	e.cat.menu[day] = menu()
	e.deliver(contracts.SubjectMenuReady, contracts.MenuReady{RequestID: "m", From: day, To: day})

	e.callOK("POST", "/v1/recommendations", 1, contracts.CreateRecommendationRequest{Date: day}, nil)
	e.advance(4 * time.Minute)
	e.tick()
	var ready contracts.RecommendationReady
	e.pub.last(t, contracts.SubjectRecommendationReady, &ready)
	if ready.UserID != 1 || len(ready.Picks) != 4 {
		t.Fatalf("picks from the score after the llm timeout: %+v", ready)
	}
	// llm answers late: ignored
	var llmReq contracts.RecommendationRequested
	e.pub.last(t, contracts.SubjectRecommendationRequested, &llmReq)
	e.deliver(contracts.SubjectRecommendationGenerated, contracts.RecommendationGenerated{RequestID: llmReq.RequestID, Picks: []contracts.GeneratedPick{{ID: 1}}})
	if n := len(e.pub.all(contracts.SubjectRecommendationReady)); n != 1 {
		t.Fatalf("late llm answer: %d", n)
	}

	// broker down: picks from the score, sent to the bot when it is back
	e.pub.fail = true
	e.callOK("POST", "/v1/recommendations", 2, contracts.CreateRecommendationRequest{Date: day}, nil)
	var rec contracts.Recommendation
	e.callOK("GET", "/v1/recommendations?date="+day, 2, nil, &rec)
	if rec.Status != "ready" {
		t.Fatalf("status %q", rec.Status)
	}
	e.pub.fail = false
	e.tick()
	e.pub.last(t, contracts.SubjectRecommendationReady, &ready)
	if ready.UserID != 2 {
		t.Fatalf("republished: %+v", ready)
	}
	e.tick()
	if n := len(e.pub.all(contracts.SubjectRecommendationReady)); n != 2 {
		t.Fatalf("published %d times", n)
	}
}

func TestCatalogDownThenBack(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LLMEnabled = false })
	day := "2026-11-02"
	e.cat.menu[day] = menu()
	e.cat.down = true
	e.deliver(contracts.SubjectMenuReady, contracts.MenuReady{RequestID: "m", From: day, To: day})
	var rec contracts.Recommendation
	e.callOK("POST", "/v1/recommendations", 1, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if rec.Status != "waiting_menu" {
		t.Fatalf("catalog down: %+v", rec)
	}
	e.cat.down = false
	e.tick()
	e.callOK("GET", "/v1/recommendations?date="+day, 1, nil, &rec)
	if rec.Status != "ready" || len(rec.Picks) == 0 {
		t.Fatalf("after catalog is back: %+v", rec)
	}
}

func TestRatingQuestion(t *testing.T) {
	e := newEnv(t)
	e.cat.menu["2026-10-29"] = menu()
	var o contracts.Order
	e.callOK("POST", "/v1/orders", 3, contracts.CreateOrderRequest{Date: "2026-10-29", DishID: 1}, &o)
	e.at(time.Date(2026, 10, 30, 9, 0, 0, 0, loc))
	e.tick()
	if n := len(e.pub.all(contracts.SubjectRatingRequested)); n != 0 {
		t.Fatalf("too early: %d", n)
	}
	e.at(time.Date(2026, 10, 30, 10, 5, 0, 0, loc))
	e.tick()
	e.tick()
	var rr contracts.RatingRequested
	e.pub.last(t, contracts.SubjectRatingRequested, &rr)
	if n := len(e.pub.all(contracts.SubjectRatingRequested)); n != 1 || rr.UserID != 3 || rr.OrderID != o.ID {
		t.Fatalf("rating question: %d %+v", n, rr)
	}
}

func TestBadEvents(t *testing.T) {
	e := newEnv(t)
	if err := e.ev.Handle(context.Background(), "food.menu.ready", []byte("{")); !errors.Is(err, bus.ErrPermanent) {
		t.Fatalf("bad json: %v", err)
	}
	raw, _ := json.Marshal(contracts.Envelope{ID: "x", Type: "food.unknown", Data: []byte(`{}`)})
	if err := e.ev.Handle(context.Background(), "food.unknown", raw); !errors.Is(err, bus.ErrPermanent) {
		t.Fatalf("unknown type: %v", err)
	}
	raw, _ = json.Marshal(contracts.Envelope{ID: "y", Type: contracts.SubjectMenuReady, Data: []byte(`{"from":"2026-11-02"}`)})
	if err := e.ev.Handle(context.Background(), contracts.SubjectMenuReady, raw); !errors.Is(err, bus.ErrPermanent) {
		t.Fatalf("missing request_id: %v", err)
	}
	// answers to requests core never made are acked and ignored
	e.deliver(contracts.SubjectMenuError, contracts.MenuError{RequestID: "nobody", Code: "no_menu"})
	e.deliver(contracts.SubjectRecommendationGenerated, contracts.RecommendationGenerated{RequestID: "nobody"})
	e.deliver(contracts.SubjectFeedbackParsed, contracts.FeedbackParsed{OrderID: 404})
}

func TestNewID(t *testing.T) {
	now := time.Now()
	a, b := bus.NewID(now), bus.NewID(now.Add(time.Millisecond))
	if len(a) != 26 || a == b || a >= b {
		t.Fatalf("ids %q %q", a, b)
	}
}
