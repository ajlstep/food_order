package api_test

// End-to-end tests of core without NATS, catalog or users: a real SQLite file
// in a temp dir, a fake catalog and users, a publisher that records the
// events, the events handler fed by hand and a clock the test moves. Requests
// go through the same handlers as on NATS.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"food-order/core/internal/api"
	"food-order/core/internal/config"
	"food-order/core/internal/events"
	"food-order/core/internal/service"
	"food-order/core/internal/store"
	"food-order/shared/contracts/catalog"
	contracts "food-order/shared/contracts/core"
	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
	sharedevents "food-order/shared/events"
	"food-order/shared/rpc"
	"food-order/shared/streams"
)

// ---------------------------------------------------------------- fakes

type fakeCatalog struct {
	mu   sync.Mutex
	menu map[string][]catalog.Dish
	down bool
}

func (c *fakeCatalog) Menu(_ context.Context, date string) ([]catalog.Dish, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return nil, errors.New("catalog: connection refused")
	}
	return c.menu[date], nil
}

func (c *fakeCatalog) Dishes(ctx context.Context, ids []int64, date string) ([]catalog.Dish, error) {
	all, err := c.Menu(ctx, date)
	if err != nil {
		return nil, err
	}
	var out []catalog.Dish
	for _, d := range all {
		for _, id := range ids {
			if d.ID == id {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

func (c *fakeCatalog) Similar(context.Context, []int64, []int64) (map[int64]float64, error) {
	return nil, errors.New("not here")
}

// fakeUsers answers like users: food per person, the group, the company.
type fakeUsers struct {
	mu      sync.Mutex
	food    map[string]users.FoodResponse
	due     []users.DueItem
	group   map[string]users.SnapshotResponse
	company map[string]users.Company
	down    bool
}

func (u *fakeUsers) Food(_ context.Context, id string) (users.FoodResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.food[id], nil
}

func (u *fakeUsers) Due(context.Context, time.Time, time.Duration) ([]users.DueItem, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.due, nil
}

func (u *fakeUsers) Snapshot(_ context.Context, id string) (users.SnapshotResponse, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.down {
		return users.SnapshotResponse{}, errors.New("users: no answer")
	}
	return u.group[id], nil
}

func (u *fakeUsers) Company(_ context.Context, id string) (users.Company, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	c, ok := u.company[id]
	if !ok {
		return c, rpc.NewError(users.ErrNotInCompany, "")
	}
	return c, nil
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
	h   map[string]rpc.Handler
	ev  *events.Handler
	pub *fakePub
	cat *fakeCatalog
	usr *fakeUsers
	now time.Time
	mu  sync.Mutex
}

var loc, _ = time.LoadLocation("Europe/Chisinau")

func newEnv(t *testing.T, mod ...func(*config.Config)) *env {
	t.Helper()
	cfg := config.Defaults()
	cfg.LLMEnabled = true // the tests go through the path with llm
	for _, m := range mod {
		m(&cfg)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, pub: &fakePub{}, cat: &fakeCatalog{menu: map[string][]catalog.Dish{}},
		usr: &fakeUsers{food: map[string]users.FoodResponse{}, group: map[string]users.SnapshotResponse{}, company: map[string]users.Company{}},
		now: time.Date(2026, 10, 30, 12, 0, 0, 0, loc)} // Friday
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e.svc = service.New(st, e.cat, e.usr, e.pub, cfg, log)
	e.svc.Now = func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }
	e.h = api.New(e.svc).Routes(log)
	e.ev = events.New(e.svc)
	return e
}

func (e *env) at(t time.Time)          { e.mu.Lock(); e.now = t; e.mu.Unlock() }
func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }
func (e *env) tick()                   { e.svc.Tick(context.Background()) }

// userID is how the numbers in the tests look as ids.
func userID(n int64) string {
	if n == 0 {
		return ""
	}
	return "u" + strconv.FormatInt(n, 10)
}

// route turns the path a test speaks of into a request.
func route(path string, body any) (subject string, data any) {
	u, _ := url.Parse(path)
	q := u.Query()
	period := contracts.PeriodRequest{From: q.Get("from"), To: q.Get("to")}
	switch {
	case u.Path == "/orders" && body != nil:
		return contracts.SubjectOrderCreate, body
	case u.Path == "/orders":
		return contracts.SubjectOrdersList, period
	case u.Path == "/rate":
		return contracts.SubjectOrderRate, body
	case u.Path == "/stats":
		return contracts.SubjectStats, period
	case u.Path == "/report":
		return contracts.SubjectCompanyReport, period
	case u.Path == "/recommendations" && body != nil:
		return contracts.SubjectRecommendationCreate, body
	}
	return contracts.SubjectRecommendationGet, contracts.GetRecommendationRequest{Date: q.Get("date")}
}

// call makes a request and returns the answer, or the code of the refusal.
func (e *env) call(path string, user int64, body any) (string, []byte) {
	e.t.Helper()
	subject, data := route(path, body)
	raw, err := json.Marshal(data)
	if err != nil {
		e.t.Fatal(err)
	}
	out, err := e.h[subject](context.Background(), rpc.Request{V: rpc.Version, UserID: userID(user), Data: raw})
	var refused *rpc.Error
	switch {
	case errors.As(err, &refused):
		return refused.Code, nil
	case err != nil:
		e.t.Fatalf("%s: %v", path, err)
	}
	b, _ := json.Marshal(out)
	return "", b
}

func (e *env) callOK(path string, user int64, body, out any) {
	e.t.Helper()
	code, raw := e.call(path, user, body)
	if code != "" {
		e.t.Fatalf("%s: refused: %s", path, code)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) deliver(subject string, data any) []byte {
	e.t.Helper()
	envl, err := sharedevents.NewEnvelope(subject, data, e.now)
	if err != nil {
		e.t.Fatal(err)
	}
	raw, _ := json.Marshal(envl)
	if err := e.ev.Handle(context.Background(), raw); err != nil {
		e.t.Fatalf("handle %s: %v", subject, err)
	}
	return raw
}

func fp(v float64) *float64 { return &v }

func llmNutrition(lo, hi float64) *catalog.Nutrition {
	return &catalog.Nutrition{Kcal: &catalog.Range{Min: lo, Max: hi}, ProteinG: &catalog.Range{Min: 20, Max: 30}}
}

func menu() []catalog.Dish {
	return []catalog.Dish{
		{ID: 1, Name: "Zeamă de găină", Price: fp(45), NutritionSource: "llm", Nutrition: llmNutrition(300, 400)},
		{ID: 2, Name: "Paste cu brânză", Price: fp(60), Components: []catalog.DishComponent{{Name: "brânză"}}},
		{ID: 3, Name: "Pilaf", Price: fp(55), Allergens: catalog.Allergens{Possible: []string{"gluten"}}},
		{ID: 4, Name: "Sarmale", Price: fp(63), NutritionSource: "llm", Nutrition: llmNutrition(600, 800)},
	}
}

// ---------------------------------------------------------------- tests

func TestNeedsAPerson(t *testing.T) {
	e := newEnv(t)
	if code, _ := e.call("/stats", 0, nil); code != rpc.CodeBadRequest {
		t.Fatalf("code = %q", code)
	}
}

func TestRecommendationFlow(t *testing.T) {
	e := newEnv(t)
	day := "2026-11-02"
	e.cat.menu[day] = menu()

	e.usr.food["u7"] = users.FoodResponse{Allergens: []string{"lapte"}, Prefs: []users.Pref{{Kind: users.PrefLike, Value: "sarmale"}}}

	// no menu yet: the recommendation waits and core asks for the menu
	var rec contracts.Recommendation
	e.callOK("/recommendations", 7, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if rec.Status != "waiting_menu" {
		t.Fatalf("status %q", rec.Status)
	}
	var mreq food.MenuRequested
	e.pub.last(t, food.SubjectMenuRequested, &mreq)
	if mreq.From != day || mreq.RequestID == "" {
		t.Fatalf("menu request %+v", mreq)
	}
	// asking again (bot scheduler + /meniu) does not double anything
	e.callOK("/recommendations", 7, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != 1 {
		t.Fatalf("menu requested %d times", n)
	}

	// the menu is in catalog -> scored, top goes to llm
	e.deliver(food.SubjectMenuReady, food.MenuReady{RequestID: mreq.RequestID, From: day, To: day, DishIDs: []int64{1, 2, 3, 4}})
	var llmReq contracts.RecommendationRequested
	e.pub.last(t, food.SubjectRecommendationRequested, &llmReq)
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
	e.callOK("/recommendations?date="+day, 7, nil, &rec)
	if rec.Status != "generating" {
		t.Fatalf("status %q", rec.Status)
	}

	// llm answers; an id that was not in the list and a duplicate are dropped
	raw := e.deliver(food.SubjectRecommendationGenerated, contracts.RecommendationGenerated{RequestID: llmReq.RequestID,
		Picks: []contracts.GeneratedPick{{ID: 999, Reason: "invented"}, {ID: 4, Reason: "Îți plac sarmalele"}, {ID: 1, Reason: "ceva cald"}, {ID: 4, Reason: "dup"}}})
	var ready contracts.RecommendationReady
	e.pub.last(t, food.SubjectRecommendationReady, &ready)
	if ready.UserID != "u7" || ready.Date != day || len(ready.Picks) != 2 || ready.Picks[0].DishID != 4 ||
		ready.Picks[0].Reason != "Îți plac sarmalele" || ready.Picks[1].Name != "Zeamă de găină" || *ready.Picks[1].KcalMax != 400 {
		t.Fatalf("ready: %+v", ready)
	}
	// the same event again (redelivery) does nothing
	if err := e.ev.Handle(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if n := len(e.pub.all(food.SubjectRecommendationReady)); n != 1 {
		t.Fatalf("ready published %d times", n)
	}

	e.callOK("/recommendations?date="+day, 7, nil, &rec)
	if rec.Status != "ready" || len(rec.Picks) != 2 {
		t.Fatalf("rec: %+v", rec)
	}
	if code, _ := e.call("/recommendations?date="+day, 8, nil); code != "not_found" {
		t.Fatalf("other user: %s", code)
	}

	// a menu that is already ready: the next user gets it at once (possible allergens shown)
	e.callOK("/recommendations", 8, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if rec.Status != "generating" {
		t.Fatalf("second user: %+v", rec)
	}
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != 1 {
		t.Fatalf("menu asked again: %d", n)
	}

	// bad input
	if code, _ := e.call("/recommendations", 7, contracts.CreateRecommendationRequest{Date: "2026-10-01"}); code != "past_date" {
		t.Fatalf("past: %s", code)
	}
	if code, _ := e.call("/recommendations", 7, map[string]string{"date": "mâine"}); code != "bad_date" {
		t.Fatalf("bad date: %s", code)
	}
}

func TestPossibleAllergensReachTheBot(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LLMEnabled = false })
	day := "2026-11-02"
	e.cat.menu[day] = menu()
	e.deliver(food.SubjectMenuReady, food.MenuReady{RequestID: "pushed", From: day, To: day})
	var rec contracts.Recommendation
	e.callOK("/recommendations", 3, contracts.CreateRecommendationRequest{Date: day}, &rec)
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
	if len(e.pub.all(food.SubjectRecommendationRequested)) != 0 {
		t.Fatal("llm must not be asked")
	}
}

func TestOrdersRatingStats(t *testing.T) {
	e := newEnv(t)
	day := "2026-10-31"
	e.cat.menu[day] = menu()

	var o contracts.Order
	e.callOK("/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 4}, &o)
	if o.ID == 0 || o.DishName != "Sarmale" || *o.Price != 63 || *o.KcalMin != 600 || *o.ProteinGMax != 30 || o.NutritionSource != "llm" {
		t.Fatalf("order: %+v", o)
	}
	// changed my mind: same order, other dish, price from the bot wins
	var o2 contracts.Order
	e.callOK("/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 3, Price: fp(50), Forced: true}, &o2)
	if o2.ID != o.ID || o2.DishName != "Pilaf" || *o2.Price != 50 || !o2.Forced || o2.NutritionSource != "missing" || o2.KcalMin != nil {
		t.Fatalf("changed: %+v", o2)
	}

	var rated contracts.Order
	e.callOK("/rate", 7, contracts.RateRequest{OrderID: o.ID, Rating: 2, Comment: " prea sărat "}, &rated)
	if *rated.Rating != 2 || rated.Comment != "prea sărat" {
		t.Fatalf("rated: %+v", rated)
	}
	var fpr contracts.FeedbackParseRequested
	e.pub.last(t, food.SubjectFeedbackParseRequested, &fpr)
	if fpr.OrderID != o.ID || fpr.Comment != "prea sărat" || fpr.Rating != 2 {
		t.Fatalf("feedback request: %+v", fpr)
	}
	e.deliver(food.SubjectFeedbackParsed, contracts.FeedbackParsed{OrderID: o.ID, Labels: json.RawMessage(`{"sărat":"prea mult"}`), KitchenFeedback: "Prea sărat."})
	var list contracts.OrdersResponse
	e.callOK("/orders?from=2026-10-01&to=2026-11-07", 7, nil, &list)
	if len(list.Items) != 1 || !strings.Contains(string(list.Items[0].Labels), "prea mult") {
		t.Fatalf("list: %+v", list)
	}

	if code, _ := e.call("/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 1}); code != "already_rated" {
		t.Fatalf("after rating: %s", code)
	}
	if code, _ := e.call("/rate", 7, contracts.RateRequest{OrderID: o.ID, Rating: 7}); code != "bad_rating" {
		t.Fatalf("bad rating: %s", code)
	}
	if code, _ := e.call("/rate", 8, contracts.RateRequest{OrderID: o.ID, Rating: 5}); code != "not_found" {
		t.Fatalf("other user's order: %s", code)
	}
	if code, _ := e.call("/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 77}); code != "dish_not_in_menu" {
		t.Fatalf("unknown dish: %s", code)
	}
	e.cat.down = true
	if code, _ := e.call("/orders", 9, contracts.CreateOrderRequest{Date: day, DishID: 4}); code != "catalog_unavailable" {
		t.Fatalf("catalog down: %s", code)
	}
	e.cat.down = false

	// stats: min and max summed apart, the order without values counted apart
	e.callOK("/orders", 9, contracts.CreateOrderRequest{Date: "2026-10-31", DishID: 4}, nil)
	e.cat.menu["2026-11-02"] = menu()
	e.callOK("/orders", 9, contracts.CreateOrderRequest{Date: "2026-11-02", DishID: 1}, nil)
	e.cat.menu["2026-11-03"] = menu()
	e.callOK("/orders", 9, contracts.CreateOrderRequest{Date: "2026-11-03", DishID: 3, Forced: true}, nil)
	var st contracts.Stats
	e.callOK("/stats?from=2026-10-01&to=2026-11-30", 9, nil, &st)
	if st.Orders != 3 || st.Spent != 163 || st.Forced != 1 || st.WithoutNutrition != 1 ||
		st.Kcal == nil || st.Kcal.Min != 900 || st.Kcal.Max != 1200 || len(st.Days) != 3 || len(st.Months) != 2 || len(st.TopDishes) != 3 {
		t.Fatalf("stats: %+v", st)
	}
	if code, _ := e.call("/stats?from=2026-11-30&to=2026-11-01", 9, nil); code != "bad_period" {
		t.Fatalf("bad period: %s", code)
	}
}

func TestOrderSnapshotWhenCatalogIsDown(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LLMEnabled = false })
	day := "2026-11-02"
	e.cat.menu[day] = menu()
	e.deliver(food.SubjectMenuReady, food.MenuReady{RequestID: "m", From: day, To: day})
	e.callOK("/recommendations", 5, contracts.CreateRecommendationRequest{Date: day}, nil)
	e.cat.down = true
	var o contracts.Order
	e.callOK("/orders", 5, contracts.CreateOrderRequest{Date: day, DishID: 1}, &o)
	if o.DishName != "Zeamă de găină" || *o.KcalMax != 400 || *o.Price != 45 {
		t.Fatalf("snapshot from the saved candidates: %+v", o)
	}
}

func TestMenuScheduleAndRetries(t *testing.T) {
	e := newEnv(t)
	e.at(time.Date(2026, 10, 30, 14, 0, 0, 0, loc)) // Friday, before 15:00
	e.tick()
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != 0 {
		t.Fatalf("too early: %d", n)
	}
	e.at(time.Date(2026, 10, 30, 15, 1, 0, 0, loc))
	e.tick()
	e.tick()
	reqs := e.pub.all(food.SubjectMenuRequested)
	var mreq food.MenuRequested
	_ = json.Unmarshal(reqs[0], &mreq)
	if len(reqs) != 1 || mreq.From != "2026-11-02" {
		t.Fatalf("Friday asks for Monday once: %d %+v", len(reqs), mreq)
	}

	// no_menu: retried after the interval, with the same request_id
	e.deliver(food.SubjectMenuError, food.MenuError{RequestID: mreq.RequestID, Code: "no_menu"})
	e.advance(20 * time.Minute)
	e.tick()
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != 1 {
		t.Fatalf("retried too early: %d", n)
	}
	e.advance(11 * time.Minute)
	e.tick()
	reqs = e.pub.all(food.SubjectMenuRequested)
	var again food.MenuRequested
	_ = json.Unmarshal(reqs[len(reqs)-1], &again)
	if len(reqs) != 2 || again.RequestID != mreq.RequestID {
		t.Fatalf("retry: %d %+v", len(reqs), again)
	}

	// past the deadline (Sunday 20:00 for Monday): alert and stop
	e.at(time.Date(2026, 11, 1, 19, 50, 0, 0, loc))
	e.deliver(food.SubjectMenuError, food.MenuError{RequestID: mreq.RequestID, Code: "no_menu"})
	var alert food.Alert
	e.pub.last(t, food.SubjectAlert, &alert)
	if alert.Service != "core" || !strings.Contains(alert.Text, "2026-11-02") {
		t.Fatalf("alert: %+v", alert)
	}
	e.advance(2 * time.Hour)
	e.tick()
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != 2 {
		t.Fatalf("asked after the deadline: %d", n)
	}
	// ...but a user asking for a recommendation asks again
	e.callOK("/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-02"}, nil)
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != 3 {
		t.Fatalf("on demand: %d", n)
	}
}

func TestMenuParseFailedAndTimeouts(t *testing.T) {
	e := newEnv(t)
	e.callOK("/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-03"}, nil)
	var mreq food.MenuRequested
	e.pub.last(t, food.SubjectMenuRequested, &mreq)
	e.deliver(food.SubjectMenuError, food.MenuError{RequestID: mreq.RequestID, Code: "parse_failed", Message: "no cards"})
	if n := len(e.pub.all(food.SubjectAlert)); n != 1 {
		t.Fatalf("parse_failed alerts at once: %d", n)
	}
	before := len(e.pub.all(food.SubjectMenuRequested))
	e.advance(time.Hour)
	e.tick()
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != before {
		t.Fatal("parse_failed must not be retried")
	}

	// but the user asks again (/meniu) while the old recommendation still
	// waits for the menu: the menu is asked again, once
	var rec contracts.Recommendation
	e.callOK("/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-03"}, &rec)
	if rec.Status != "waiting_menu" {
		t.Fatalf("status %q", rec.Status)
	}
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != before+1 {
		t.Fatalf("/meniu after parse_failed must ask for the menu again: %d sends, want %d", n, before+1)
	}
	var again food.MenuRequested
	e.pub.last(t, food.SubjectMenuRequested, &again)
	if again.RequestID == mreq.RequestID || again.From != "2026-11-03" {
		t.Errorf("a new request for the day is expected: %+v (old %s)", again, mreq.RequestID)
	}
	// asked once more while that request is still out: nothing new
	e.callOK("/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-03"}, nil)
	if n := len(e.pub.all(food.SubjectMenuRequested)); n != before+1 {
		t.Fatalf("a request in flight must not be doubled: %d sends", n)
	}
	// the menu comes: the waiting recommendation is generated
	e.cat.menu["2026-11-03"] = menu()
	e.deliver(food.SubjectMenuReady, food.MenuReady{RequestID: again.RequestID, From: "2026-11-03", To: "2026-11-03"})
	e.callOK("/recommendations?date=2026-11-03", 1, nil, &rec)
	if rec.Status == "waiting_menu" {
		t.Errorf("still waiting after the menu came: %+v", rec)
	}

	// nobody answers: expired -> retried with a growing pause -> one alert after 5 sends
	e2 := newEnv(t)
	e2.callOK("/recommendations", 1, contracts.CreateRecommendationRequest{Date: "2026-11-04"}, nil)
	for i := 0; i < 30; i++ {
		e2.advance(11 * time.Minute)
		e2.tick()
	}
	sends := len(e2.pub.all(food.SubjectMenuRequested))
	var alerts [][]byte // (the daily request for Monday also starts on the way; look only at ours)
	for _, a := range e2.pub.all(food.SubjectAlert) {
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
	e.deliver(food.SubjectMenuReady, food.MenuReady{RequestID: "m", From: day, To: day})

	e.callOK("/recommendations", 1, contracts.CreateRecommendationRequest{Date: day}, nil)
	e.advance(4 * time.Minute)
	e.tick()
	var ready contracts.RecommendationReady
	e.pub.last(t, food.SubjectRecommendationReady, &ready)
	if ready.UserID != "u1" || len(ready.Picks) != 4 {
		t.Fatalf("picks from the score after the llm timeout: %+v", ready)
	}
	// llm answers late: ignored
	var llmReq contracts.RecommendationRequested
	e.pub.last(t, food.SubjectRecommendationRequested, &llmReq)
	e.deliver(food.SubjectRecommendationGenerated, contracts.RecommendationGenerated{RequestID: llmReq.RequestID, Picks: []contracts.GeneratedPick{{ID: 1}}})
	if n := len(e.pub.all(food.SubjectRecommendationReady)); n != 1 {
		t.Fatalf("late llm answer: %d", n)
	}

	// broker down: picks from the score, sent to the bot when it is back
	e.pub.fail = true
	e.callOK("/recommendations", 2, contracts.CreateRecommendationRequest{Date: day}, nil)
	var rec contracts.Recommendation
	e.callOK("/recommendations?date="+day, 2, nil, &rec)
	if rec.Status != "ready" {
		t.Fatalf("status %q", rec.Status)
	}
	e.pub.fail = false
	e.tick()
	e.pub.last(t, food.SubjectRecommendationReady, &ready)
	if ready.UserID != "u2" {
		t.Fatalf("republished: %+v", ready)
	}
	e.tick()
	if n := len(e.pub.all(food.SubjectRecommendationReady)); n != 2 {
		t.Fatalf("published %d times", n)
	}
}

func TestCatalogDownThenBack(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LLMEnabled = false })
	day := "2026-11-02"
	e.cat.menu[day] = menu()
	e.cat.down = true
	e.deliver(food.SubjectMenuReady, food.MenuReady{RequestID: "m", From: day, To: day})
	var rec contracts.Recommendation
	e.callOK("/recommendations", 1, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if rec.Status != "waiting_menu" {
		t.Fatalf("catalog down: %+v", rec)
	}
	e.cat.down = false
	e.tick()
	e.callOK("/recommendations?date="+day, 1, nil, &rec)
	if rec.Status != "ready" || len(rec.Picks) == 0 {
		t.Fatalf("after catalog is back: %+v", rec)
	}
}

func TestRatingQuestion(t *testing.T) {
	e := newEnv(t)
	e.cat.menu["2026-10-29"] = menu()
	var o contracts.Order
	e.callOK("/orders", 3, contracts.CreateOrderRequest{Date: "2026-10-29", DishID: 1}, &o)
	e.at(time.Date(2026, 10, 30, 9, 0, 0, 0, loc))
	e.tick()
	if n := len(e.pub.all(food.SubjectRatingRequested)); n != 0 {
		t.Fatalf("too early: %d", n)
	}
	e.at(time.Date(2026, 10, 30, 10, 5, 0, 0, loc))
	e.tick()
	e.tick()
	var rr contracts.RatingRequested
	e.pub.last(t, food.SubjectRatingRequested, &rr)
	if n := len(e.pub.all(food.SubjectRatingRequested)); n != 1 || rr.UserID != "u3" || rr.OrderID != o.ID {
		t.Fatalf("rating question: %d %+v", n, rr)
	}
}

func TestBadEvents(t *testing.T) {
	e := newEnv(t)
	if err := e.ev.Handle(context.Background(), []byte("{")); !errors.Is(err, streams.ErrDrop) {
		t.Fatalf("bad json: %v", err)
	}
	raw, _ := json.Marshal(sharedevents.Envelope{ID: "x", Type: "food.unknown", Data: []byte(`{}`)})
	if err := e.ev.Handle(context.Background(), raw); !errors.Is(err, streams.ErrDrop) {
		t.Fatalf("unknown type: %v", err)
	}
	raw, _ = json.Marshal(sharedevents.Envelope{ID: "y", Type: food.SubjectMenuReady, Data: []byte(`{"from":"2026-11-02"}`)})
	if err := e.ev.Handle(context.Background(), raw); !errors.Is(err, streams.ErrDrop) {
		t.Fatalf("missing request_id: %v", err)
	}
	// answers to requests core never made are acked and ignored
	e.deliver(food.SubjectMenuError, food.MenuError{RequestID: "nobody", Code: "no_menu"})
	e.deliver(food.SubjectRecommendationGenerated, contracts.RecommendationGenerated{RequestID: "nobody"})
	e.deliver(food.SubjectFeedbackParsed, contracts.FeedbackParsed{OrderID: 404})
}

func TestOrderTimeStartsARecommendationOnce(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LLMEnabled = false })
	day := "2026-11-02"
	e.cat.menu[day] = menu()
	e.deliver(food.SubjectMenuReady, food.MenuReady{RequestID: "m", From: day, To: day})
	e.usr.due = []users.DueItem{{UserID: "u7", ForDate: day}}

	e.tick()
	e.tick()
	if n := len(e.pub.all(food.SubjectRecommendationReady)); n != 1 {
		t.Fatalf("recommendations sent = %d", n)
	}
}

func TestOrderGoesToTheGroupUnlessPersonal(t *testing.T) {
	e := newEnv(t)
	day := "2026-10-31"
	e.cat.menu[day] = menu()
	e.usr.group["u7"] = users.SnapshotResponse{GroupID: "g1", GroupName: "Etaj 3", CompanyID: "c1", CompanyName: "Acme", Address: "Str. Mare 1"}

	var o contracts.Order
	e.callOK("/orders", 7, contracts.CreateOrderRequest{Date: day, DishID: 4}, &o)
	if o.GroupName != "Etaj 3" || o.CompanyName != "Acme" || o.Address != "Str. Mare 1" {
		t.Fatalf("order: %+v", o)
	}
	e.cat.menu["2026-11-02"] = menu()
	var personal contracts.Order
	e.callOK("/orders", 7, contracts.CreateOrderRequest{Date: "2026-11-02", DishID: 1, Personal: true}, &personal)
	if personal.GroupName != "" || personal.Address != "" {
		t.Fatalf("personal order: %+v", personal)
	}

	// the report of the company has only the first one, and only for its owner
	e.usr.company["u1"] = users.Company{ID: "c1", IsOwner: true}
	e.usr.company["u7"] = users.Company{ID: "c1"}
	var rep contracts.CompanyReport
	e.callOK("/report?from=2026-10-01&to=2026-11-30", 1, nil, &rep)
	if len(rep.Items) != 1 || rep.Items[0].UserID != "u7" || rep.Items[0].DishName != "Sarmale" || rep.Spent != 63 {
		t.Fatalf("report: %+v", rep)
	}
	if code, _ := e.call("/report?from=2026-10-01&to=2026-11-30", 7, nil); code != users.ErrNotOwner {
		t.Fatalf("member: %s", code)
	}
	if code, _ := e.call("/report?from=2026-10-01&to=2026-11-30", 9, nil); code != users.ErrNotInCompany {
		t.Fatalf("no company: %s", code)
	}

	e.usr.down = true
	if code, _ := e.call("/orders", 8, contracts.CreateOrderRequest{Date: day, DishID: 4}); code != "users_unavailable" {
		t.Fatalf("users down: %s", code)
	}
}

func TestOrderLimitOfThePartner(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.LLMEnabled = false })
	day := "2026-11-02"
	limit := e.now.Add(time.Hour).UnixMilli()
	e.cat.menu[day] = []catalog.Dish{
		{ID: 1, Name: "Deschis", PartnerName: "Bistro", OrderUntilMs: limit, Components: []catalog.DishComponent{{Name: "Supă"}, {Name: "Piure"}}},
		{ID: 2, Name: "Închis", Closed: true},
		{ID: 3, Name: "Trecut", OrderUntilMs: e.now.Add(-time.Hour).UnixMilli()},
	}
	e.deliver(food.SubjectMenuReady, food.MenuReady{RequestID: "m", From: day, To: day})

	var rec contracts.Recommendation
	e.callOK("/recommendations", 1, contracts.CreateRecommendationRequest{Date: day}, &rec)
	if len(rec.Picks) != 1 || rec.Picks[0].DishID != 1 || rec.Picks[0].OrderUntilMs != limit || rec.Picks[0].PartnerName != "Bistro" ||
		len(rec.Picks[0].Components) != 2 || rec.Picks[0].Components[1] != "Piure" {
		t.Fatalf("picks: %+v", rec.Picks)
	}
	if code, _ := e.call("/orders", 1, contracts.CreateOrderRequest{Date: day, DishID: 3}); code != "order_closed" {
		t.Fatalf("past the limit: %q", code)
	}
	if code, _ := e.call("/orders", 1, contracts.CreateOrderRequest{Date: day, DishID: 2}); code != "order_closed" {
		t.Fatalf("closed: %q", code)
	}
	e.advance(2 * time.Hour)
	if code, _ := e.call("/orders", 1, contracts.CreateOrderRequest{Date: day, DishID: 1}); code != "order_closed" {
		t.Fatalf("after the limit: %q", code)
	}
}
