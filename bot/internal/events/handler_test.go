package events

import (
	"context"
	"errors"
	"testing"

	"food-order/bot/internal/contracts"
)

type fakeNotifier struct {
	recs    []contracts.RecommendationReady
	ratings []contracts.RatingRequested
	alerts  []contracts.Alert
	fail    error
}

func (f *fakeNotifier) RecommendationReady(_ context.Context, r contracts.RecommendationReady) error {
	if f.fail != nil {
		return f.fail
	}
	f.recs = append(f.recs, r)
	return nil
}

func (f *fakeNotifier) RatingRequested(_ context.Context, r contracts.RatingRequested) error {
	f.ratings = append(f.ratings, r)
	return nil
}

func (f *fakeNotifier) Alert(_ context.Context, a contracts.Alert) error {
	f.alerts = append(f.alerts, a)
	return nil
}

const recEvent = `{"id":"01A","type":"food.recommendation.ready","version":1,"ts":"2026-10-28T15:00:00+02:00",
 "data":{"date":"2026-10-29","picks":[{"dish_id":12,"name":"Ciorbă de pui","price":45,"reason":"ți-a plăcut"}]}}`

func TestHandleRecommendationAndDuplicate(t *testing.T) {
	n := &fakeNotifier{}
	h := NewHandler(n)
	for i := 0; i < 2; i++ {
		if err := h.Handle(context.Background(), []byte(recEvent)); err != nil {
			t.Fatal(err)
		}
	}
	if len(n.recs) != 1 {
		t.Fatalf("got %d notifications, want 1 (duplicate must be ignored)", len(n.recs))
	}
	if p := n.recs[0].Picks[0]; p.DishID != 12 || *p.Price != 45 {
		t.Fatalf("pick = %+v", p)
	}
}

func TestHandleFailureIsRetried(t *testing.T) {
	n := &fakeNotifier{fail: errors.New("telegram down")}
	h := NewHandler(n)
	err := h.Handle(context.Background(), []byte(recEvent))
	if err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("err = %v, want a temporary error", err)
	}
	n.fail = nil
	if err := h.Handle(context.Background(), []byte(recEvent)); err != nil {
		t.Fatal(err)
	}
	if len(n.recs) != 1 {
		t.Fatal("event must be handled after the failure")
	}
}

func TestHandlePermanent(t *testing.T) {
	h := NewHandler(&fakeNotifier{})
	for _, raw := range []string{
		`not json`,
		`{"id":"1","type":"food.something.else","data":{}}`,
		`{"id":"2","type":"food.alert","data":"oops"}`,
		`{"id":"3","type":"food.alert","version":2,"data":{}}`,
	} {
		if err := h.Handle(context.Background(), []byte(raw)); !errors.Is(err, ErrPermanent) {
			t.Errorf("%s: err = %v, want ErrPermanent", raw, err)
		}
	}
}

func TestHandleAlertAndRating(t *testing.T) {
	n := &fakeNotifier{}
	h := NewHandler(n)
	_ = h.Handle(context.Background(), []byte(`{"id":"a","type":"food.alert","version":1,"data":{"service":"core","level":"error","text":"no menu"}}`))
	_ = h.Handle(context.Background(), []byte(`{"id":"b","type":"food.rating.requested","version":1,"data":{"order_id":9}}`))
	if len(n.alerts) != 1 || n.alerts[0].Service != "core" {
		t.Fatalf("alerts = %+v", n.alerts)
	}
	if len(n.ratings) != 1 || n.ratings[0].OrderID != 9 {
		t.Fatalf("ratings = %+v", n.ratings)
	}
}

func TestSeenSetForgetsOldest(t *testing.T) {
	s := newSeenSet(2)
	s.add("a")
	s.add("b")
	s.add("c")
	if s.has("a") || !s.has("b") || !s.has("c") {
		t.Fatal("seenSet must keep only the last 2 ids")
	}
}
