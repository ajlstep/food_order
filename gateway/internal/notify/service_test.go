package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/notify"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
	"food-order/shared/rpc"
	"food-order/shared/streams"
)

type fakeDirectory struct {
	channels users.ChannelsResponse
	settings map[string]string
	err      error
}

func (f *fakeDirectory) Channels(context.Context, string, string) (users.ChannelsResponse, error) {
	return f.channels, f.err
}

func (f *fakeDirectory) Settings(context.Context, string, string) (map[string]string, error) {
	return f.settings, nil
}

type put struct {
	channel string
	m       notify.Message
}

type fakeQueue struct {
	puts []put
	err  error
}

func (q *fakeQueue) Put(_ context.Context, channel string, m notify.Message) error {
	if q.err != nil {
		return q.err
	}
	q.puts = append(q.puts, put{channel, m})
	return nil
}

func telegramOnly(status string) users.ChannelsResponse {
	return users.ChannelsResponse{
		Channels: []users.Channel{{Channel: "telegram", ChatRef: "42", Status: status}},
		Latest:   "telegram",
	}
}

func event(t *testing.T, kind, data string) []byte {
	t.Helper()
	b, err := json.Marshal(events.Envelope{ID: "ev1", Type: kind, Version: 1, Data: json.RawMessage(data)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newService(d Directory, q Queue) *Service {
	return NewService(d, q, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

const forPerson = `{"user_id":"u-1","date":"2026-11-04","picks":[]}`

func TestDelivery(t *testing.T) {
	cases := []struct {
		name     string
		dir      fakeDirectory
		raw      string
		wantPuts int
	}{
		{"active channel", fakeDirectory{channels: telegramOnly(users.IdentityActive)}, forPerson, 1},
		{"channel stopped with /stop", fakeDirectory{channels: telegramOnly(users.IdentityStopped)}, forPerson, 0},
		{"personal notifications off", fakeDirectory{
			channels: telegramOnly(users.IdentityActive),
			settings: map[string]string{users.SettingNotifyPersonal: "false"},
		}, forPerson, 0},
		{"no such account", fakeDirectory{err: rpc.NewError(rpc.CodeNotFound, "")}, forPerson, 0},
		{"blocked account", fakeDirectory{err: rpc.NewError(users.ErrAccountBlocked, "")}, forPerson, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := &fakeQueue{}
			s := newService(&c.dir, q)
			if err := s.Handle(context.Background(), event(t, food.SubjectRecommendationReady, c.raw)); err != nil {
				t.Fatal(err)
			}
			if len(q.puts) != c.wantPuts {
				t.Fatalf("puts = %+v", q.puts)
			}
		})
	}
}

func TestMessageCarriesTheEventData(t *testing.T) {
	q := &fakeQueue{}
	s := newService(&fakeDirectory{channels: telegramOnly(users.IdentityActive)}, q)
	if err := s.Handle(context.Background(), event(t, food.SubjectRatingRequested, `{"user_id":"u-1","order_id":5}`)); err != nil {
		t.Fatal(err)
	}
	got := q.puts[0]
	if got.channel != "telegram" || got.m.Kind != notify.KindRating || got.m.ChatRef != "42" || got.m.UserID != "u-1" ||
		got.m.ID != "ev1.telegram" || !strings.Contains(string(got.m.Data), `"order_id":5`) {
		t.Fatalf("put = %+v", got)
	}
}

func TestChooseChannels(t *testing.T) {
	c := users.ChannelsResponse{
		Channels: []users.Channel{
			{Channel: "telegram", ChatRef: "1", Status: users.IdentityActive},
			{Channel: "whatsapp", ChatRef: "2", Status: users.IdentityActive},
			{Channel: "viber", ChatRef: "3", Status: users.IdentityStopped},
		},
		Latest: "whatsapp",
	}
	normal := chooseChannels(c, notify.ClassNormal)
	if len(normal) != 1 || normal[0].Channel != "whatsapp" {
		t.Fatalf("normal = %+v", normal)
	}
	if important := chooseChannels(c, notify.ClassImportant); len(important) != 2 {
		t.Fatalf("important = %+v", important)
	}
}

func TestAlertGoesWithoutAPerson(t *testing.T) {
	q := &fakeQueue{}
	s := newService(&fakeDirectory{err: errors.New("users must not be asked")}, q)
	if err := s.Handle(context.Background(), event(t, food.SubjectAlert, `{"service":"core","text":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if len(q.puts) != 1 || q.puts[0].m.Kind != notify.KindAlert || q.puts[0].m.ChatRef != "" {
		t.Fatalf("puts = %+v", q.puts)
	}
}

func TestDropped(t *testing.T) {
	s := newService(&fakeDirectory{}, &fakeQueue{})
	cases := map[string][]byte{
		"not json":      []byte("{"),
		"unknown event": event(t, "food.something", `{}`),
		"no user_id":    event(t, food.SubjectRatingRequested, `{"order_id":1}`),
		// what core sends until stage 7
		"user_id as a number": event(t, food.SubjectRatingRequested, `{"user_id":3,"order_id":1}`),
	}
	for name, raw := range cases {
		if err := s.Handle(context.Background(), raw); !errors.Is(err, streams.ErrDrop) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestTriedAgain(t *testing.T) {
	raw := event(t, food.SubjectRecommendationReady, forPerson)
	for _, code := range []string{rpc.CodeUnavailable, rpc.CodeTimeout, rpc.CodeInternal} {
		s := newService(&fakeDirectory{err: rpc.NewError(code, "")}, &fakeQueue{})
		if err := s.Handle(context.Background(), raw); err == nil || errors.Is(err, streams.ErrDrop) {
			t.Errorf("%s: err = %v", code, err)
		}
	}
	s := newService(&fakeDirectory{channels: telegramOnly(users.IdentityActive)}, &fakeQueue{err: errors.New("nats is down")})
	if err := s.Handle(context.Background(), raw); err == nil || errors.Is(err, streams.ErrDrop) {
		t.Errorf("queue down: err = %v", err)
	}
}
