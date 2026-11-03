package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	contracts "food-order/shared/contracts/core"
	"food-order/shared/contracts/food"
	"food-order/shared/contracts/notify"
	"food-order/shared/streams"
)

type fakeSender struct {
	calls []string
	err   error
}

func (f *fakeSender) RecommendationReady(_ context.Context, to Recipient, r contracts.RecommendationReady) error {
	f.calls = append(f.calls, "rec "+to.ChatRef+" "+r.Date)
	return f.err
}

func (f *fakeSender) RatingRequested(_ context.Context, to Recipient, _ contracts.RatingRequested) error {
	f.calls = append(f.calls, "rating "+to.UserID)
	return f.err
}

func (f *fakeSender) OrgInvite(_ context.Context, to Recipient, d food.OrgInvite) error {
	f.calls = append(f.calls, "invite "+to.ChatRef+" "+d.Name)
	return f.err
}

func (f *fakeSender) OrgNotice(_ context.Context, to Recipient, d food.OrgNotice) error {
	f.calls = append(f.calls, "notice "+to.ChatRef+" "+d.Notice)
	return f.err
}

func (f *fakeSender) Alert(_ context.Context, a food.Alert) error {
	f.calls = append(f.calls, "alert "+a.Text)
	return f.err
}

func message(t *testing.T, m notify.Message) []byte {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newHandler(s Sender) *Handler {
	return NewHandler(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestKinds(t *testing.T) {
	s := &fakeSender{}
	h := newHandler(s)
	ctx := context.Background()
	cases := []notify.Message{
		{ID: "1", Kind: notify.KindRecommendation, ChatRef: "42", Data: []byte(`{"date":"2026-10-29"}`)},
		{ID: "2", Kind: notify.KindRating, UserID: "u-1", Data: []byte(`{"order_id":5}`)},
		{ID: "3", Kind: notify.KindAlert, Data: []byte(`{"text":"x"}`)},
		{ID: "4", Kind: notify.KindOrgInvite, ChatRef: "42", Data: []byte(`{"name":"Acme"}`)},
		{ID: "5", Kind: notify.KindOrgNotice, ChatRef: "42", Data: []byte(`{"notice":"removed"}`)},
	}
	for _, m := range cases {
		if err := h.Handle(ctx, message(t, m)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"rec 42 2026-10-29", "rating u-1", "alert x", "invite 42 Acme", "notice 42 removed"}
	if !slices.Equal(s.calls, want) {
		t.Fatalf("calls = %q", s.calls)
	}
}

func TestDuplicateIsShownOnce(t *testing.T) {
	s := &fakeSender{}
	h := newHandler(s)
	m := message(t, notify.Message{ID: "1", Kind: notify.KindAlert, Data: []byte(`{}`)})
	for range 2 {
		if err := h.Handle(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.calls) != 1 {
		t.Fatalf("calls = %q", s.calls)
	}
}

func TestFailureIsTriedAgain(t *testing.T) {
	s := &fakeSender{err: errors.New("telegram is down")}
	h := newHandler(s)
	m := message(t, notify.Message{ID: "1", Kind: notify.KindAlert, Data: []byte(`{}`)})
	err := h.Handle(context.Background(), m)
	if err == nil || errors.Is(err, streams.ErrDrop) {
		t.Fatalf("err = %v, want one that is tried again", err)
	}
	s.err = nil
	if err := h.Handle(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 2 {
		t.Fatal("a message that failed must not count as handled")
	}
}

func TestBadMessagesAreDropped(t *testing.T) {
	h := newHandler(&fakeSender{})
	cases := map[string][]byte{
		"not json":     []byte("{"),
		"unknown kind": message(t, notify.Message{Kind: "nou", Data: []byte(`{}`)}),
		"bad data":     message(t, notify.Message{Kind: notify.KindRating, Data: []byte(`{"order_id":"x"}`)}),
	}
	for name, raw := range cases {
		if err := h.Handle(context.Background(), raw); !errors.Is(err, streams.ErrDrop) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
