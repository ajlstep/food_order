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
	"log/slog"
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
	"core": "pw-core", "catalog": "pw-catalog", "parser": "pw-parser",
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

// The notifications: the gateway reads events only through its consumer and
// writes only to the queues; the bot reads its queue. The same path as in
// production, with the real permissions.
func TestNotificationPath(t *testing.T) {
	url := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	js, err := jetstream.New(connect(t, url, "admin").nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish(ctx, "food.alert", []byte("event")); err != nil {
		t.Fatal(err)
	}

	gw := connect(t, url, "gateway")
	read := func(user *client, stream, consumer string) string {
		t.Helper()
		out := make(chan string, 1)
		cctx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			_ = streams.Consume(cctx, user.nc, stream, consumer, func(_ context.Context, data []byte) error {
				out <- string(data)
				return nil
			}, slog.New(slog.DiscardHandler))
		}()
		select {
		case got := <-out:
			return got
		case <-ctx.Done():
			t.Fatalf("nothing arrived on %s", consumer)
			return ""
		}
	}
	if got := read(gw, streams.Food, streams.ConsumerNotifier); got != "event" {
		t.Fatalf("the gateway read %q", got)
	}

	gwJS, err := jetstream.New(gw.nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gwJS.Publish(ctx, "notify.telegram", []byte("message")); err != nil {
		t.Fatalf("the gateway cannot write to the queue: %v", err)
	}
	if got := read(connect(t, url, "bot"), streams.Notify, streams.ConsumerBotTelegram); got != "message" {
		t.Fatalf("the bot read %q", got)
	}
}

func TestGatewayCannotReadOtherConsumers(t *testing.T) {
	url := setup(t)
	gw := connect(t, url, "gateway")
	js, err := jetstream.New(gw.nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := js.Consumer(ctx, streams.Notify, streams.ConsumerBotTelegram); err == nil {
		t.Fatal("the gateway must not reach the consumer of the bot")
	}
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

func TestNobodyWithoutAUser(t *testing.T) {
	url := setup(t)
	if nc, err := nats.Connect(url); err == nil {
		nc.Close()
		t.Fatal("a connection without a user was accepted")
	}
}

// The logger of every service asks JetStream if the stream LOGS exists; that
// needs an inbox to receive the answer. (Found by running the real service.)
func TestServicesCanCheckTheLogsStream(t *testing.T) {
	url := setup(t)
	for _, user := range []string{"users", "gateway", "bot", "core", "catalog", "parser"} {
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

// published says whether a user may put this event on the stream.
func published(t *testing.T, url, user, subject string) error {
	t.Helper()
	js, err := jetstream.New(connect(t, url, user).nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = js.Publish(ctx, subject, []byte("{}"))
	return err
}

// Everybody publishes only the events that are theirs.
func TestEventsHaveOneOwner(t *testing.T) {
	url := setup(t)
	allowed := map[string][]string{
		"core":    {"food.menu.requested", "food.recommendation.ready", "food.rating.requested", "food.alert"},
		"catalog": {"food.menu.ready", "food.dish.analyze.requested", "food.alert"},
		"parser":  {"food.menu.fetched", "food.menu.error"},
		"users":   {"food.org.invite", "food.org.notice"},
	}
	for user, subjects := range allowed {
		for _, s := range subjects {
			if err := published(t, url, user, s); err != nil {
				t.Errorf("%s cannot publish %s: %v", user, s, err)
			}
		}
	}
	forbidden := map[string]string{
		"core": "food.org.invite", "catalog": "food.menu.fetched", "parser": "food.recommendation.ready",
		"users": "food.alert", "bot": "food.alert", "gateway": "food.org.notice",
	}
	for user, s := range forbidden {
		if err := published(t, url, user, s); err == nil {
			t.Errorf("%s may publish %s", user, s)
		}
	}
}

// Each service reads only its own consumer of FOOD.
func TestServicesReadTheirOwnConsumer(t *testing.T) {
	url := setup(t)
	for _, name := range []string{"core", "catalog", "parser"} {
		js, err := jetstream.New(connect(t, url, name).nc)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		c, err := js.Consumer(ctx, streams.Food, name)
		if err != nil {
			t.Errorf("%s cannot reach its consumer: %v", name, err)
			cancel()
			continue
		}
		msgs, err := c.Fetch(1, jetstream.FetchMaxWait(200*time.Millisecond))
		if err != nil {
			t.Errorf("%s cannot read its consumer: %v", name, err)
		} else if err := msgs.Error(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		other := "core"
		if name == "core" {
			other = "parser"
		}
		short, stop := context.WithTimeout(context.Background(), 500*time.Millisecond)
		if _, err := js.Consumer(short, streams.Food, other); err == nil {
			t.Errorf("%s reached the consumer of %s", name, other)
		}
		stop()
		cancel()
	}
}

// core asks users and catalog only what it needs; the rest of users is
// reached through the gateway, for the person who wrote.
func TestCoreAsksOnlyWhatItNeeds(t *testing.T) {
	url := setup(t)
	u := connect(t, url, "users")
	cat := connect(t, url, "catalog")
	for _, s := range []struct {
		c       *client
		subject string
	}{{u, "rpc.users.profile.food.get"}, {u, "rpc.users.profile.due"}, {u, "rpc.users.org.snapshot"},
		{u, "rpc.users.org.company.get"}, {cat, "rpc.catalog.menu.get"}, {cat, "rpc.catalog.similar"}} {
		if _, err := s.c.nc.Subscribe(s.subject, func(m *nats.Msg) { _ = m.Respond([]byte("{}")) }); err != nil {
			t.Fatal(err)
		}
		if err := s.c.nc.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	core := connect(t, url, "core")
	for _, s := range []string{"rpc.users.profile.food.get", "rpc.users.profile.due", "rpc.users.org.snapshot", "rpc.catalog.similar"} {
		if _, err := core.nc.Request(s, []byte("{}"), 2*time.Second); err != nil {
			t.Errorf("core cannot ask %s: %v", s, err)
		}
	}
	if err := core.nc.Publish("rpc.users.profile.get", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	core.forbidden(t, "core asking for a whole profile")
}

func TestCoreAndCatalogListenOnlyToThemselves(t *testing.T) {
	url := setup(t)
	c := connect(t, url, "core")
	if _, err := c.nc.Subscribe("rpc.users.>", func(*nats.Msg) {}); err != nil {
		t.Fatal(err)
	}
	c.forbidden(t, "core listening to users")
	p := connect(t, url, "parser")
	if _, err := p.nc.Subscribe("rpc.core.>", func(*nats.Msg) {}); err != nil {
		t.Fatal(err)
	}
	p.forbidden(t, "parser listening to core")
}
