package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"food-order/catalog/internal/config"
	"food-order/catalog/internal/contracts"
	"food-order/catalog/internal/store"
)

// fakeBus records what is published.
type fakeBus struct {
	mu   sync.Mutex
	sent []sent
	down bool
}

type sent struct {
	subject string
	data    json.RawMessage
}

func (b *fakeBus) Publish(_ context.Context, subject string, data any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.down {
		return io.ErrClosedPipe
	}
	raw, _ := json.Marshal(data)
	b.sent = append(b.sent, sent{subject, raw})
	return nil
}

// take returns and forgets what was published on subject.
func (b *fakeBus) take(subject string) []json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []json.RawMessage
	rest := b.sent[:0]
	for _, s := range b.sent {
		if s.subject == subject {
			out = append(out, s.data)
		} else {
			rest = append(rest, s)
		}
	}
	b.sent = rest
	return out
}

type env struct {
	t   *testing.T
	svc *Service
	bus *fakeBus
	now time.Time
	ctx context.Context
}

func newEnv(t *testing.T, change func(*config.Config)) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Defaults()
	if change != nil {
		change(&cfg)
	}
	e := &env{t: t, bus: &fakeBus{}, now: time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC), ctx: context.Background()}
	e.svc = New(st, e.bus, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.svc.Now = func() time.Time { return e.now }
	return e
}

func ptr(f float64) *float64 { return &f }

func parserMenu(reqID, date string, items ...contracts.SourceItem) contracts.MenuFetched {
	return contracts.MenuFetched{RequestID: reqID, Source: "parser", Days: []contracts.MenuDay{{Date: date, Items: items}}}
}

var (
	stand = contracts.SourceItem{Name: "LUNCH 1 Stand", Category: "Lunch Box", Price: ptr(63),
		Components: []contracts.Component{{Name: "Orez cu legume"}, {Name: "Pui la grătar"}}}
	maxi = contracts.SourceItem{Name: "LUNCH 1  Maxi", Category: "Lunch Box", Price: ptr(75),
		Components: []contracts.Component{{Name: "Supă de linte"}, {Name: "Orez cu legume"}, {Name: "Pui la grătar"}}}
)

func (e *env) importMenu(m contracts.MenuFetched) contracts.ImportResponse {
	e.t.Helper()
	out, err := e.svc.Import(e.ctx, m)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *env) requests() []contracts.DishAnalyzeRequested {
	var out []contracts.DishAnalyzeRequested
	for _, raw := range e.bus.take(contracts.SubjectDishAnalyzeRequested) {
		var r contracts.DishAnalyzeRequested
		if err := json.Unmarshal(raw, &r); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *env) ready() []contracts.MenuReady {
	var out []contracts.MenuReady
	for _, raw := range e.bus.take(contracts.SubjectMenuReady) {
		var r contracts.MenuReady
		if err := json.Unmarshal(raw, &r); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e *env) menu(date string) []contracts.Dish {
	e.t.Helper()
	m, err := e.svc.Menu(e.ctx, date)
	if err != nil {
		e.t.Fatal(err)
	}
	return m.Items
}

func analysis(itemID int64, kcalMin, kcalMax float64) contracts.DishAnalyzed {
	return contracts.DishAnalyzed{
		MenuItemID:      itemID,
		TypicalPortionG: ptr(350),
		Nutrition: &contracts.Nutrition{
			Kcal:     &contracts.Range{Min: kcalMin, Max: kcalMax},
			ProteinG: &contracts.Range{Min: 30, Max: 20}, // reversed on purpose
			SaltG:    &contracts.Range{Min: -1, Max: 2},  // invalid: dropped
		},
		AllergensPossible: []string{"Gluten", "ouă", "gluten"},
		Tags:              []string{"cald", "Cu carne"},
		Model:             "qwen3:8b",
	}
}

func TestParserMenuToReady(t *testing.T) {
	e := newEnv(t, nil)
	out := e.importMenu(parserMenu("r1", "2026-11-02", stand, maxi))
	if len(out.Days) != 1 || out.Days[0].Revision != 1 || out.Days[0].Items != 2 || !out.Days[0].Changed {
		t.Fatalf("import: %+v", out)
	}

	reqs := e.requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 analysis requests, got %d", len(reqs))
	}
	// the parser gave the components: they are known, not asked
	wantNeed := []string{"nutrition", "allergens", "portion", "tags"}
	if !reflect.DeepEqual(reqs[0].Need, wantNeed) || reqs[0].Name != "LUNCH 1 Stand" || len(reqs[0].Known.Components) != 2 {
		t.Errorf("request: %+v", reqs[0])
	}
	if len(e.ready()) != 0 {
		t.Fatal("menu must not be ready before llm answers")
	}
	if st, _, _ := e.svc.Store.RequestStatus(e.ctx, "r1"); st != "waiting" {
		t.Errorf("request status %q", st)
	}
	// no embedding before the analysis is over
	if n := len(e.bus.take(contracts.SubjectDishEmbedRequested)); n != 0 {
		t.Errorf("%d embedding requests too early", n)
	}

	if err := e.svc.OnAnalyzed(e.ctx, analysis(reqs[0].MenuItemID, 600, 800)); err != nil {
		t.Fatal(err)
	}
	if len(e.ready()) != 0 {
		t.Fatal("one dish is still waiting")
	}
	if err := e.svc.OnAnalyzed(e.ctx, analysis(reqs[1].MenuItemID, 800, 1000)); err != nil {
		t.Fatal(err)
	}
	ready := e.ready()
	if len(ready) != 1 || ready[0].RequestID != "r1" || ready[0].From != "2026-11-02" || ready[0].To != "2026-11-02" ||
		!reflect.DeepEqual(ready[0].DishIDs, []int64{reqs[0].DishID, reqs[1].DishID}) {
		t.Fatalf("ready: %+v", ready)
	}
	if n := len(e.bus.take(contracts.SubjectDishEmbedRequested)); n != 2 {
		t.Errorf("want 2 embedding requests, got %d", n)
	}

	items := e.menu("2026-11-02")
	if len(items) != 2 {
		t.Fatalf("menu: %d items", len(items))
	}
	d := items[0]
	if d.Name != "LUNCH 1 Stand" || d.WeightG == nil || *d.WeightG != 350 || d.WeightSource != "llm" ||
		d.NutritionSource != "llm" || d.Nutrition == nil || d.Nutrition.Kcal.Min != 600 || d.Nutrition.Kcal.Max != 800 {
		t.Errorf("dish: %+v", d)
	}
	if d.Nutrition.ProteinG.Min != 20 || d.Nutrition.ProteinG.Max != 30 || d.Nutrition.SaltG != nil {
		t.Errorf("nutrition not cleaned: %+v %+v", d.Nutrition.ProteinG, d.Nutrition.SaltG)
	}
	if !reflect.DeepEqual(d.Allergens.Possible, []string{"gluten", "ouă"}) || d.Allergens.Confirmed != nil ||
		!reflect.DeepEqual(d.Tags, []string{"cald", "cu carne"}) || d.Status != "analyzed" || d.Analysis != "done" {
		t.Errorf("dish info: %+v", d)
	}
	if len(d.Components) != 2 || d.Components[0].Source != "site" {
		t.Errorf("components: %+v", d.Components)
	}
	// a second flush publishes nothing new
	if err := e.svc.Flush(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.ready()) != 0 || len(e.requests()) != 0 {
		t.Error("flush published twice")
	}
}

func TestSameDishNextDayReusesAnalysis(t *testing.T) {
	e := newEnv(t, nil)
	e.importMenu(parserMenu("r1", "2026-11-02", stand))
	reqs := e.requests()
	if err := e.svc.OnAnalyzed(e.ctx, analysis(reqs[0].MenuItemID, 600, 800)); err != nil {
		t.Fatal(err)
	}
	e.ready()

	e.importMenu(parserMenu("r2", "2026-11-03", stand))
	if n := len(e.requests()); n != 0 {
		t.Fatalf("the same dish with the same input must not be analysed again (%d requests)", n)
	}
	ready := e.ready()
	if len(ready) != 1 || ready[0].RequestID != "r2" || ready[0].DishIDs[0] != reqs[0].DishID {
		t.Fatalf("ready: %+v", ready)
	}
	d := e.menu("2026-11-03")[0]
	if d.NutritionSource != "llm" || d.Nutrition.Kcal.Max != 800 || d.WeightG == nil || *d.WeightG != 350 || d.Analysis != "done" {
		t.Errorf("not copied: %+v", d)
	}
}

func TestOneRequestForTheSameDishOnSeveralDays(t *testing.T) {
	e := newEnv(t, nil)
	e.importMenu(contracts.MenuFetched{RequestID: "r1", Days: []contracts.MenuDay{
		{Date: "2026-11-03", Items: []contracts.SourceItem{stand}},
		{Date: "2026-11-02", Items: []contracts.SourceItem{stand}},
	}})
	reqs := e.requests()
	if len(reqs) != 1 {
		t.Fatalf("want one request for both days, got %d", len(reqs))
	}
	if err := e.svc.OnAnalyzed(e.ctx, analysis(reqs[0].MenuItemID, 1, 2)); err != nil {
		t.Fatal(err)
	}
	ready := e.ready()
	if len(ready) != 1 || ready[0].From != "2026-11-02" || ready[0].To != "2026-11-03" {
		t.Fatalf("ready: %+v", ready)
	}
	for _, date := range []string{"2026-11-02", "2026-11-03"} {
		if d := e.menu(date)[0]; d.NutritionSource != "llm" {
			t.Errorf("%s: %+v", date, d)
		}
	}
}

func TestRevisions(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AnalysisEnabled = false })
	e.importMenu(parserMenu("r1", "2026-11-02", stand, maxi))
	if len(e.ready()) != 1 {
		t.Fatal("with analysis off the menu is ready at once")
	}
	if d := e.menu("2026-11-02")[0]; d.Analysis != "off" || d.NutritionSource != "missing" {
		t.Errorf("off: %+v", d)
	}

	// the same content again: same revision, nothing changed
	out := e.importMenu(parserMenu("r1", "2026-11-02", stand, maxi))
	if out.Days[0].Revision != 1 || out.Days[0].Changed {
		t.Errorf("same content: %+v", out.Days[0])
	}

	// maxi gone, stand cheaper: a new revision replaces the old one
	cheaper := stand
	cheaper.Price = ptr(60)
	out = e.importMenu(parserMenu("r3", "2026-11-02", cheaper))
	if out.Days[0].Revision != 2 || !out.Days[0].Changed {
		t.Errorf("new revision: %+v", out.Days[0])
	}
	items := e.menu("2026-11-02")
	if len(items) != 1 || *items[0].Price != 60 || items[0].Revision != 2 {
		t.Fatalf("after revision: %+v", items)
	}

	// an explicitly older revision is ignored
	old := parserMenu("r4", "2026-11-02", stand, maxi)
	old.Revision = 1
	out = e.importMenu(old)
	if !out.Days[0].Skipped || len(e.menu("2026-11-02")) != 1 {
		t.Errorf("older revision not ignored: %+v", out.Days[0])
	}
}

func TestChangedInputDropsEstimates(t *testing.T) {
	e := newEnv(t, nil)
	e.importMenu(parserMenu("r1", "2026-11-02", stand))
	reqs := e.requests()
	if err := e.svc.OnAnalyzed(e.ctx, analysis(reqs[0].MenuItemID, 600, 800)); err != nil {
		t.Fatal(err)
	}
	e.ready()

	other := stand
	other.Components = []contracts.Component{{Name: "Hrișcă"}, {Name: "Pui la grătar"}}
	e.importMenu(parserMenu("r2", "2026-11-02", other))
	reqs = e.requests()
	if len(reqs) != 1 || !reflect.DeepEqual(reqs[0].Need, []string{"nutrition", "portion"}) {
		t.Fatalf("a changed dish must be analysed again (only what is per day): %+v", reqs)
	}
	d := e.menu("2026-11-02")[0]
	if d.NutritionSource != "missing" || d.WeightG != nil || d.Analysis != "requested" {
		t.Errorf("old estimates kept: %+v", d)
	}
	if len(d.Allergens.Possible) == 0 {
		t.Error("dish-level allergens are kept")
	}
}

func TestKitchenSourcePer100g(t *testing.T) {
	e := newEnv(t, nil)
	item := contracts.SourceItem{
		ExternalID: "k-17", Name: "Ciorbă de pui", WeightG: ptr(300),
		Components: []contracts.Component{{Name: "piept de pui", Grams: ptr(90), Method: "boiled"}, {Name: "morcov", Method: "Fiert la abur"}},
		Nutrition:  json.RawMessage(`{"per": "100g", "kcal": 40, "protein_g": {"min": 3, "max": 4}}`),
		Allergens:  []string{},
	}
	e.importMenu(contracts.MenuFetched{RequestID: "r1", Source: "api", Days: []contracts.MenuDay{{Date: "2026-11-02", Items: []contracts.SourceItem{item}}}})
	reqs := e.requests()
	if len(reqs) != 1 || !reflect.DeepEqual(reqs[0].Need, []string{"tags"}) {
		t.Fatalf("only tags are missing: %+v", reqs)
	}
	if reqs[0].Known.Nutrition == nil || reqs[0].Known.Nutrition.Kcal.Min != 120 {
		t.Errorf("known nutrition: %+v", reqs[0].Known)
	}
	// llm tries to give nutrition anyway: the kitchen's values win
	a := analysis(reqs[0].MenuItemID, 999, 999)
	if err := e.svc.OnAnalyzed(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	d := e.menu("2026-11-02")[0]
	if d.NutritionSource != "kitchen" || d.Nutrition.Kcal.Min != 120 || d.Nutrition.ProteinG.Max != 12 ||
		*d.WeightG != 300 || d.WeightSource != "kitchen" || d.Source != "api" {
		t.Errorf("kitchen values: %+v %+v", d, d.Nutrition)
	}
	if d.Allergens.Given == nil || len(d.Allergens.Given) != 0 {
		t.Errorf("given 'none' must stay an empty list: %#v", d.Allergens.Given)
	}
	if d.Components[0].Source != "kitchen" || d.Components[0].Method != "boiled" || d.Components[1].Method != "other" {
		t.Errorf("components: %+v", d.Components)
	}
	if len(e.ready()) != 1 {
		t.Error("not ready")
	}
}

func TestPer100WithoutWeightWaitsForPortion(t *testing.T) {
	e := newEnv(t, nil)
	item := contracts.SourceItem{Name: "Plăcintă cu brânză", Nutrition: json.RawMessage(`{"per": "100 g", "kcal": 300}`)}
	e.importMenu(parserMenu("r1", "2026-11-02", item))
	reqs := e.requests()
	if len(reqs) != 1 || !reflect.DeepEqual(reqs[0].Need, []string{"components", "allergens", "portion", "tags"}) {
		t.Fatalf("need: %+v", reqs)
	}
	a := analysis(reqs[0].MenuItemID, 1, 1)
	a.TypicalPortionG = ptr(150)
	if err := e.svc.OnAnalyzed(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	d := e.menu("2026-11-02")[0]
	if d.NutritionSource != "kitchen" || d.Nutrition.Kcal.Min != 450 || d.WeightSource != "llm" {
		t.Errorf("per 100 g not scaled by the estimated portion: %+v %+v", d, d.Nutrition)
	}
}

func TestWeightFromText(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AnalysisEnabled = false })
	item := contracts.SourceItem{Name: "Friptură", Weight: "250/50"}
	e.importMenu(parserMenu("r1", "2026-11-02", item))
	d := e.menu("2026-11-02")[0]
	if d.WeightG == nil || *d.WeightG != 300 || d.WeightText != "250/50" || d.WeightSource != "site" {
		t.Errorf("weight: %+v", d)
	}
}

func TestLlmTimeout(t *testing.T) {
	e := newEnv(t, nil)
	e.importMenu(parserMenu("r1", "2026-11-02", stand))
	reqs := e.requests()
	e.now = e.now.Add(e.svc.Cfg.AnalysisTimeout + time.Minute)
	if err := e.svc.Flush(e.ctx); err != nil {
		t.Fatal(err)
	}
	if len(e.bus.take(contracts.SubjectAlert)) != 1 {
		t.Error("no alert")
	}
	if len(e.ready()) != 1 {
		t.Fatal("the menu must go ready with what there is")
	}
	if d := e.menu("2026-11-02")[0]; d.Analysis != "timeout" || d.NutritionSource != "missing" {
		t.Errorf("after timeout: %+v", d)
	}
	// a late answer is still saved
	if err := e.svc.OnAnalyzed(e.ctx, analysis(reqs[0].MenuItemID, 5, 6)); err != nil {
		t.Fatal(err)
	}
	if d := e.menu("2026-11-02")[0]; d.Analysis != "done" || d.NutritionSource != "llm" {
		t.Errorf("late answer: %+v", d)
	}
	if len(e.ready()) != 0 {
		t.Error("menu.ready sent twice")
	}
}

func TestBrokerDownThenUp(t *testing.T) {
	e := newEnv(t, nil)
	e.bus.down = true
	e.importMenu(parserMenu("r1", "2026-11-02", stand))
	if d := e.menu("2026-11-02")[0]; d.Analysis != "pending" {
		t.Fatalf("with the broker down the request stays pending: %+v", d)
	}
	e.bus.down = false
	e.svc.Tick(e.ctx)
	if len(e.requests()) != 1 {
		t.Error("the request did not go out after the broker came back")
	}
}

func TestOldRevisionAnswerIgnored(t *testing.T) {
	e := newEnv(t, nil)
	e.importMenu(parserMenu("r1", "2026-11-02", stand, maxi))
	reqs := e.requests()
	e.importMenu(parserMenu("r2", "2026-11-02", stand)) // maxi is gone
	gone := reqs[1].MenuItemID
	if err := e.svc.OnAnalyzed(e.ctx, analysis(gone, 1, 2)); err != nil {
		t.Fatalf("an answer for a removed item is acked, not an error: %v", err)
	}
}

func TestSimilar(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AnalysisEnabled = false })
	soup := contracts.SourceItem{Name: "Supă de pui"}
	soup2 := contracts.SourceItem{Name: "Zeamă de găină"}
	cake := contracts.SourceItem{Name: "Tort"}
	e.importMenu(parserMenu("r1", "2026-11-02", soup, soup2, cake))
	embeds := e.bus.take(contracts.SubjectDishEmbedRequested)
	if len(embeds) != 3 {
		t.Fatalf("want 3 embedding requests, got %d", len(embeds))
	}
	var first contracts.DishEmbedRequested
	_ = json.Unmarshal(embeds[0], &first)
	if first.Text != "Supă de pui" {
		t.Errorf("embed text %q", first.Text)
	}
	ids := map[string]int64{}
	for _, d := range e.menu("2026-11-02") {
		ids[d.Name] = d.ID
	}
	vec := map[string][]float64{"Supă de pui": {1, 0.1, 0}, "Zeamă de găină": {0.9, 0.2, 0}, "Tort": {0, 0, 1}}
	for name, v := range vec {
		if err := e.svc.OnEmbedded(e.ctx, contracts.DishEmbedded{DishID: ids[name], Model: "bge-m3", Vector: v}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := e.svc.Similar(e.ctx, contracts.SimilarRequest{
		LikedIDs:     []int64{ids["Supă de pui"]},
		CandidateIDs: []int64{ids["Zeamă de găină"], ids["Tort"], ids["Supă de pui"], 999},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Scores) != 2 { // the liked dish is not compared with itself; 999 has no embedding
		t.Fatalf("scores: %+v", out.Scores)
	}
	if out.Scores[0].Score < 0.95 || out.Scores[1].Score != 0 || out.Scores[0].LikeID != ids["Supă de pui"] {
		t.Errorf("scores: %+v", out.Scores)
	}
	if err := e.svc.OnEmbedded(e.ctx, contracts.DishEmbedded{DishID: ids["Tort"], Vector: nil}); err == nil {
		t.Error("an empty vector is an error")
	}
}

func TestConfirmAllergens(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AnalysisEnabled = false })
	e.importMenu(parserMenu("r1", "2026-11-02", stand))
	id := e.menu("2026-11-02")[0].ID
	d, err := e.svc.ConfirmAllergens(e.ctx, 7, id, []string{"Lapte", "lapte", " gluten "})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allergens.Confirmed == nil || !reflect.DeepEqual(*d.Allergens.Confirmed, []string{"lapte", "gluten"}) || d.Status != "confirmed" {
		t.Errorf("confirmed: %+v", d.Allergens)
	}
	d, err = e.svc.ConfirmAllergens(e.ctx, 7, id, []string{})
	if err != nil || d.Allergens.Confirmed == nil || len(*d.Allergens.Confirmed) != 0 {
		t.Errorf("confirmed none: %+v %v", d.Allergens, err)
	}
	if _, err := e.svc.ConfirmAllergens(e.ctx, 7, 999, []string{}); err == nil {
		t.Error("unknown dish")
	}
	if _, err := e.svc.ConfirmAllergens(e.ctx, 7, id, nil); err == nil {
		t.Error("missing list")
	}
}

func TestDishes(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.AnalysisEnabled = false })
	e.importMenu(parserMenu("r1", "2026-11-02", stand, maxi))
	cheaper := stand
	cheaper.Price = ptr(50)
	e.importMenu(parserMenu("r2", "2026-11-03", cheaper))
	items := e.menu("2026-11-02")
	standID, maxiID := items[0].ID, items[1].ID

	out, err := e.svc.Dishes(e.ctx, "12345, "+itoa(standID)+","+itoa(maxiID), "2026-11-03")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].ID != standID || *out.Items[0].Price != 50 {
		t.Errorf("by date: %+v", out.Items)
	}
	out, _ = e.svc.Dishes(e.ctx, itoa(standID)+","+itoa(maxiID), "")
	if len(out.Items) != 2 || out.Items[0].Date != "2026-11-03" || out.Items[1].Date != "2026-11-02" {
		t.Errorf("latest day: %+v", out.Items)
	}
	if _, err := e.svc.Dish(e.ctx, maxiID, "2026-11-03"); err == nil {
		t.Error("maxi is not on the menu of 2026-11-03")
	}
	if _, err := e.svc.Dishes(e.ctx, "1,x", ""); err == nil {
		t.Error("bad ids")
	}
	if _, err := e.svc.Menu(e.ctx, "02.11.2026"); err == nil {
		t.Error("bad date")
	}
	m, err := e.svc.Menu(e.ctx, "") // tomorrow in Europe/Chisinau
	if err != nil || m.Date != "2026-10-31" {
		t.Errorf("tomorrow: %q %v", m.Date, err)
	}
}

func TestBadMenu(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.svc.Import(e.ctx, contracts.MenuFetched{RequestID: "r"}); err == nil {
		t.Error("no days")
	}
	if _, err := e.svc.Import(e.ctx, parserMenu("r", "mâine", stand)); err == nil {
		t.Error("bad date")
	}
	if _, err := e.svc.Import(e.ctx, parserMenu("r", "2026-11-02", contracts.SourceItem{Name: "  "})); err == nil {
		t.Error("no named dish")
	}
	// a menu nobody asked for gets its own request id
	out := e.importMenu(parserMenu("", "2026-11-02", stand))
	if out.RequestID == "" {
		t.Error("no request id")
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
