package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data", "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func f(v float64) *float64 { return &v }

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func TestReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(path); err != nil { // migrations run again without harm
		t.Fatal(err)
	}
	s.Close()
}

func TestOrders(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	o, err := s.SaveOrder(ctx, Order{UserID: 7, Date: "2026-10-31", DishID: 12, Price: f(63),
		Snapshot: Snapshot{DishName: "LUNCH 1 Stand", KcalMin: f(600), KcalMax: f(800), NutritionSource: "llm"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if o.ID == 0 || o.DishName != "LUNCH 1 Stand" || *o.KcalMax != 800 || o.Rating != nil {
		t.Fatalf("saved: %+v", o)
	}

	// a second choice the same day replaces the first
	o2, err := s.SaveOrder(ctx, Order{UserID: 7, Date: "2026-10-31", DishID: 15, Forced: true,
		Snapshot: Snapshot{DishName: "LUNCH 2", NutritionSource: "missing"}}, now)
	if err != nil || o2.ID != o.ID || o2.DishID != 15 || !o2.Forced || o2.KcalMin != nil {
		t.Fatalf("replace: %+v %v", o2, err)
	}

	// other users don't see it
	if _, err := s.Order(ctx, 8, o.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user: %v", err)
	}
	if _, err := s.Rate(ctx, 8, o.ID, 5, "", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user rate: %v", err)
	}

	r, err := s.Rate(ctx, 7, o.ID, 4, "prea sărat", now)
	if err != nil || *r.Rating != 4 || r.Comment != "prea sărat" {
		t.Fatalf("rate: %+v %v", r, err)
	}
	if _, err := s.SaveOrder(ctx, Order{UserID: 7, Date: "2026-10-31", DishID: 18, Snapshot: Snapshot{DishName: "x"}}, now); !errors.Is(err, ErrRated) {
		t.Fatalf("change after rating: %v", err)
	}
	if err := s.SetLabels(ctx, o.ID, []byte(`{"sărat":"prea mult"}`), "Prea sărat.", now); err != nil {
		t.Fatal(err)
	}
	list, err := s.Orders(ctx, 7, "2026-10-01", "2026-10-31")
	if err != nil || len(list) != 1 || list[0].Labels == "" || list[0].KitchenFeedback != "Prea sărat." {
		t.Fatalf("list: %+v %v", list, err)
	}
}

func TestRatingQuestions(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	a, _ := s.SaveOrder(ctx, Order{UserID: 1, Date: "2026-10-29", DishID: 1, Snapshot: Snapshot{DishName: "a"}}, now)
	_, _ = s.SaveOrder(ctx, Order{UserID: 2, Date: "2026-10-30", DishID: 1, Snapshot: Snapshot{DishName: "a"}}, now)
	list, err := s.OrdersToAskRating(ctx, "2026-10-27", "2026-10-29")
	if err != nil || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("to ask: %+v %v", list, err)
	}
	_ = s.MarkRatingRequested(ctx, a.ID, now)
	if list, _ = s.OrdersToAskRating(ctx, "2026-10-27", "2026-10-29"); len(list) != 0 {
		t.Fatalf("asked twice: %+v", list)
	}
}

func TestPrefs(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.ReplacePrefs(ctx, 3, []Pref{{Kind: "allergen", Value: "nuci", Hard: true}, {Kind: "like", Value: "ciorbă"}}); err != nil {
		t.Fatal(err)
	}
	_ = s.ReplacePrefs(ctx, 4, []Pref{{Kind: "like", Value: "pește"}})
	ps, err := s.Prefs(ctx, 3)
	if err != nil || len(ps) != 2 || !ps[0].Hard || ps[1].Value != "ciorbă" {
		t.Fatalf("prefs: %+v %v", ps, err)
	}
	_ = s.ReplacePrefs(ctx, 3, nil)
	if ps, _ = s.Prefs(ctx, 3); len(ps) != 0 {
		t.Fatalf("cleared: %+v", ps)
	}
	if ps, _ = s.Prefs(ctx, 4); len(ps) != 1 {
		t.Fatalf("other user touched: %+v", ps)
	}
}

func TestRecommendations(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	r, err := s.CreateRec(ctx, 5, "2026-10-31", "req-1", now)
	if err != nil || r.Status != RecWaitingMenu {
		t.Fatalf("create: %+v %v", r, err)
	}
	if ok, _ := s.ClaimRec(ctx, r.ID, RecWaitingMenu, RecGenerating, now); !ok {
		t.Fatal("first claim must win")
	}
	if ok, _ := s.ClaimRec(ctx, r.ID, RecWaitingMenu, RecGenerating, now); ok {
		t.Fatal("second claim must lose")
	}
	_ = s.SetCandidates(ctx, r.ID, []Candidate{{DishID: 12, Name: "Zeamă", Price: f(50)}})
	if ok, _ := s.FinishRec(ctx, r.ID, []PickRef{{DishID: 12, Reason: "îți place"}}, now); !ok {
		t.Fatal("finish")
	}
	if ok, _ := s.FinishRec(ctx, r.ID, nil, now); ok {
		t.Fatal("finish twice")
	}
	got, err := s.RecByRequestID(ctx, "req-1")
	if err != nil || got.Status != RecReady || len(got.Picks) != 1 || got.Candidates[0].Name != "Zeamă" {
		t.Fatalf("read: %+v %v", got, err)
	}
	un, _ := s.RecsUnnotified(ctx, "2026-10-30")
	if len(un) != 1 {
		t.Fatalf("unnotified: %+v", un)
	}
	_ = s.MarkNotified(ctx, r.ID, now)
	if un, _ = s.RecsUnnotified(ctx, "2026-10-30"); len(un) != 0 {
		t.Fatalf("still unnotified: %+v", un)
	}
	if latest, _ := s.LatestReadyRecsFor(ctx, "2026-10-31"); len(latest) != 1 {
		t.Fatalf("latest ready: %+v", latest)
	}
}

func TestMenus(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	m := MenuRequest{RequestID: "m1", DateFrom: "2026-11-02", DateTo: "2026-11-02", Status: MenuRequested,
		Attempts: 1, RequestedAt: now.Format(TimeFormat)}
	if err := s.CreateMenuRequest(ctx, m, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.LatestMenuFor(ctx, "2026-11-02")
	if err != nil || got.RequestID != "m1" || !got.Active() {
		t.Fatalf("latest: %+v %v", got, err)
	}
	old, _ := s.MenusSentBefore(ctx, now.Add(time.Minute))
	if len(old) != 1 {
		t.Fatalf("sent before: %+v", old)
	}
	got.Status, got.ErrorCode, got.NextRetryAt = MenuError, "no_menu", now.Add(30*time.Minute).Format(TimeFormat)
	_ = s.UpdateMenuRequest(ctx, got, now)
	if due, _ := s.MenusToRetry(ctx, now); len(due) != 0 {
		t.Fatalf("not due yet: %+v", due)
	}
	if due, _ := s.MenusToRetry(ctx, now.Add(31*time.Minute)); len(due) != 1 {
		t.Fatalf("due: %+v", due)
	}
	if ready, _ := s.MenuReadyFor(ctx, "2026-11-02", ""); ready {
		t.Fatal("not ready")
	}
	got.Status, got.NextRetryAt = MenuReady, ""
	_ = s.UpdateMenuRequest(ctx, got, now)
	if ready, _ := s.MenuReadyFor(ctx, "2026-11-02", ""); !ready {
		t.Fatal("ready")
	}
	if ready, _ := s.MenuReadyFor(ctx, "2026-11-02", "m1"); ready {
		t.Fatal("except itself")
	}
}

func TestProcessed(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if done, _ := s.Processed(ctx, "e1"); done {
		t.Fatal("new")
	}
	_ = s.MarkProcessed(ctx, "e1", "food.menu.ready", now)
	_ = s.MarkProcessed(ctx, "e1", "food.menu.ready", now) // twice is fine
	if done, _ := s.Processed(ctx, "e1"); !done {
		t.Fatal("seen")
	}
	_ = s.ForgetProcessed(ctx, now.Add(time.Hour))
	if done, _ := s.Processed(ctx, "e1"); done {
		t.Fatal("forgotten")
	}
}
