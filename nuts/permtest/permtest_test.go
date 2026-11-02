// Package permtest starts a real NATS server with the real nuts/nats.conf and
// checks what the conf promises: each service can do its job and nothing more,
// and request/reply works next to the streams.
//
// It is a test module of its own (go.mod) so that the nats-server library is
// not pulled into the services.
package permtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"food-order/shared/rpc"
	"food-order/shared/streams"
)

var passwords = map[string]string{
	"admin": "pw-admin", "gateway": "pw-gateway", "users": "pw-users", "bot": "pw-bot", "logs": "pw-logs",
}

// startServer runs nats.conf with the data directory in a temp folder.
func startServer(t *testing.T) string {
	t.Helper()
	for user, pw := range passwords {
		t.Setenv("NATS_PASSWORD_"+strings.ToUpper(user), pw)
	}
	conf, err := filepath.Abs("../nats.conf")
	if err != nil {
		t.Fatal(err)
	}
	opts, err := server.ProcessConfigFile(conf)
	if err != nil {
		t.Fatalf("nats.conf does not parse: %v", err)
	}
	opts.Port = -1    // any free port
	opts.HTTPPort = 0 // no monitor port
	opts.StoreDir = t.TempDir()
	opts.NoLog, opts.NoSigs = true, true

	srv, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats server did not start")
	}
	t.Cleanup(srv.Shutdown)
	return srv.ClientURL()
}

// client is a connection plus the errors the server sent about it
// (a forbidden publish or subscribe is reported asynchronously).
type client struct {
	nc   *nats.Conn
	errs chan error
}

func connect(t *testing.T, url, user string) *client {
	t.Helper()
	c := &client{errs: make(chan error, 16)}
	opts := []nats.Option{nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		select {
		case c.errs <- err:
		default:
		}
	})}
	if user != "" {
		opts = append(opts, nats.UserInfo(user, passwords[user]))
	}
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		t.Fatalf("connect as %q: %v", user, err)
	}
	t.Cleanup(nc.Close)
	c.nc = nc
	return c
}

// forbidden waits for the server to report a permissions violation.
func (c *client) forbidden(t *testing.T, what string) {
	t.Helper()
	if err := c.nc.Flush(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-c.errs:
		if !strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
			t.Fatalf("%s: expected a permissions violation, got %v", what, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was allowed, but it should be forbidden", what)
	}
}

// setup starts the server and creates the streams, as the nats-setup job does.
func setup(t *testing.T) string {
	t.Helper()
	url := startServer(t)
	js, err := jetstream.New(connect(t, url, "admin").nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := streams.Ensure(ctx, js); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return url
}

func TestEnsureCanRunTwice(t *testing.T) {
	url := setup(t)
	js, _ := jetstream.New(connect(t, url, "admin").nc)
	if err := streams.Ensure(context.Background(), js); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
}

// The risk from Gateway.md: a request on a subject close to a stream must get
// the answer of the service, not the confirmation of the stream. rpc.> is
// outside every stream, so it does.
func TestRequestReplyWorksNextToStreams(t *testing.T) {
	url := setup(t)

	usersConn := connect(t, url, "users")
	srv := rpc.NewServer(usersConn.nc, slogDiscard(), "users")
	err := srv.Handle("rpc.users.ping", func(context.Context, rpc.Request) (any, error) {
		return map[string]string{"pong": "yes"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := usersConn.nc.Flush(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var out map[string]string
	if err := rpc.NewClient(connect(t, url, "gateway").nc).Call(ctx, "rpc.users.ping", "req-1", nil, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out["pong"] != "yes" {
		t.Fatalf("got %v", out)
	}
}

func TestNobodyListening(t *testing.T) {
	url := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := rpc.NewClient(connect(t, url, "gateway").nc).Call(ctx, "rpc.users.ping", "req-2", nil, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeUnavailable {
		t.Fatalf("expected %q, got %v", rpc.CodeUnavailable, err)
	}
}

// The bot reads only its own queue, and can do nothing else.
func TestBotReadsItsQueueOnly(t *testing.T) {
	url := setup(t)

	admin := connect(t, url, "admin")
	adminJS, _ := jetstream.New(admin.nc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := adminJS.Publish(ctx, "notify.telegram", []byte(`{"text":"hello"}`)); err != nil {
		t.Fatal(err)
	}

	bot := connect(t, url, "bot")
	botJS, _ := jetstream.New(bot.nc)
	cons, err := botJS.Consumer(ctx, streams.Notify, streams.ConsumerBotTelegram)
	if err != nil {
		t.Fatalf("bot cannot open its consumer: %v", err)
	}
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for m := range batch.Messages() {
		if string(m.Data()) != `{"text":"hello"}` {
			t.Fatalf("unexpected message %s", m.Data())
		}
		if err := m.DoubleAck(ctx); err != nil {
			t.Fatalf("ack: %v", err)
		}
		n++
	}
	if n != 1 {
		t.Fatalf("expected 1 message, got %d", n)
	}

	// Forbidden requests are not answered, so give them a short deadline.
	short, cancelShort := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancelShort()

	// another consumer of the same stream: not allowed
	if _, err := botJS.Consumer(short, streams.Notify, "somebody-else"); err == nil {
		t.Fatal("bot opened a consumer that is not its own")
	}
	// the stream FOOD: not allowed
	if _, err := botJS.Stream(short, streams.Food); err == nil {
		t.Fatal("bot read the FOOD stream")
	}
}

func TestBotCannotCallServices(t *testing.T) {
	url := setup(t)
	bot := connect(t, url, "bot")
	_ = bot.nc.Publish("rpc.users.ping", []byte("{}"))
	bot.forbidden(t, "bot publishing rpc.users.ping")
}

func TestUsersCannotListenToOtherServices(t *testing.T) {
	url := setup(t)
	u := connect(t, url, "users")
	_, _ = u.nc.Subscribe("rpc.core.>", func(*nats.Msg) {})
	u.forbidden(t, "users subscribing to rpc.core.>")
}

func TestGatewayCannotPublishEvents(t *testing.T) {
	url := setup(t)
	g := connect(t, url, "gateway")
	_ = g.nc.Publish("food.recommendation.ready", []byte("{}"))
	g.forbidden(t, "gateway publishing food.*")
}

func TestUsersCanLog(t *testing.T) {
	url := setup(t)
	u := connect(t, url, "users")
	if err := u.nc.Publish("logs.info", []byte(`{"msg":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if err := u.nc.Flush(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-u.errs:
		t.Fatalf("users cannot publish logs: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestWrongPasswordIsRefused(t *testing.T) {
	url := setup(t)
	_, err := nats.Connect(url, nats.UserInfo("users", "wrong"))
	if err == nil {
		t.Fatal("a wrong password was accepted")
	}
}

// Transitional: a connection without a user still works, as "legacy".
func TestOldServicesStillConnect(t *testing.T) {
	url := setup(t)
	old := connect(t, url, "")
	js, err := jetstream.New(old.nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := js.Publish(ctx, "food.test", []byte("{}")); err != nil {
		t.Fatalf("legacy cannot publish an event: %v", err)
	}
}

var _ = os.Getenv

// The logger of every service asks JetStream if the stream LOGS exists; that
// needs an inbox to receive the answer. (Found by running the real service.)
func TestServicesCanCheckTheLogsStream(t *testing.T) {
	url := setup(t)
	for _, user := range []string{"users", "gateway", "bot"} {
		js, err := jetstream.New(connect(t, url, user).nc)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if _, err := js.Stream(ctx, streams.Logs); err != nil {
			t.Errorf("%s cannot look at the LOGS stream: %v", user, err)
		}
		cancel()
	}
}
