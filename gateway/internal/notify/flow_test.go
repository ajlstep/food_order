package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/notify"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
	"food-order/shared/rpc"
	"food-order/shared/streams"
)

// The whole way on a real NATS server with the real streams: event -> notify
// -> queue. users is fake and answers over rpc.

func startNATS(t *testing.T) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	ns, err := server.NewServer(&server.Options{Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats did not start")
	}
	t.Cleanup(ns.Shutdown)
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := streams.Ensure(ctx, js); err != nil {
		t.Fatal(err)
	}
	return nc, js
}

func fakeUsers(t *testing.T, nc *nats.Conn, failFirst *atomic.Int32) {
	t.Helper()
	srv := rpc.NewServer(nc, slog.New(slog.DiscardHandler), "users")
	err := srv.Handle(users.SubjectProfileChannelsGet, func(_ context.Context, req rpc.Request) (any, error) {
		if failFirst.Add(-1) >= 0 {
			return nil, rpc.NewError(rpc.CodeUnavailable, "starting")
		}
		if req.UserID != "u-1" {
			return nil, rpc.NewError(rpc.CodeNotFound, "")
		}
		return telegramOnly(users.IdentityActive), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = srv.Handle(users.SubjectProfileSettingsGet, func(context.Context, rpc.Request) (any, error) {
		return users.SettingsResponse{Settings: map[string]string{users.SettingNotifyPersonal: "true"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publish(t *testing.T, js jetstream.JetStream, subject, id, data string) {
	t.Helper()
	raw, _ := json.Marshal(events.Envelope{ID: id, Type: subject, Version: 1, Data: json.RawMessage(data)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := js.Publish(ctx, subject, raw); err != nil {
		t.Fatal(err)
	}
}

func nextInQueue(t *testing.T, js jetstream.JetStream, wait time.Duration) (notify.Message, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cons, err := js.Consumer(ctx, streams.Notify, streams.ConsumerBotTelegram)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(wait))
	if err != nil {
		t.Fatal(err)
	}
	for msg := range batch.Messages() {
		_ = msg.Ack()
		var m notify.Message
		if err := json.Unmarshal(msg.Data(), &m); err != nil {
			t.Fatal(err)
		}
		return m, true
	}
	return notify.Message{}, false
}

func TestEventReachesTheQueue(t *testing.T) {
	nc, js := startNATS(t)
	var failFirst atomic.Int32
	fakeUsers(t, nc, &failFirst)

	svc, err := New(nc, rpc.NewClient(nc), time.Second, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = streams.Consume(ctx, nc, streams.Food, streams.ConsumerNotifier, svc.Handle, slog.New(slog.DiscardHandler))
	}()

	publish(t, js, food.SubjectRatingRequested, "ev1", `{"user_id":"u-1","order_id":5}`)
	m, ok := nextInQueue(t, js, 5*time.Second)
	if !ok || m.Kind != notify.KindRating || m.ChatRef != "42" {
		t.Fatalf("queue: %+v %v", m, ok)
	}

	// the same event twice: NATS keeps only one message
	publish(t, js, food.SubjectRatingRequested, "ev1", `{"user_id":"u-1","order_id":5}`)
	if m, ok := nextInQueue(t, js, 700*time.Millisecond); ok {
		t.Fatalf("a second message came: %+v", m)
	}
}

func TestEventWaitsForUsers(t *testing.T) {
	nc, js := startNATS(t)
	var failFirst atomic.Int32
	failFirst.Store(1)
	fakeUsers(t, nc, &failFirst)

	svc, err := New(nc, rpc.NewClient(nc), time.Second, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = streams.Consume(ctx, nc, streams.Food, streams.ConsumerNotifier, svc.Handle, slog.New(slog.DiscardHandler))
	}()

	publish(t, js, food.SubjectRatingRequested, "ev2", `{"user_id":"u-1","order_id":5}`)
	// the first delivery fails, the retry comes after the first pause (5 s)
	if m, ok := nextInQueue(t, js, 8*time.Second); !ok || m.UserID != "u-1" {
		t.Fatalf("queue: %+v %v", m, ok)
	}
}
