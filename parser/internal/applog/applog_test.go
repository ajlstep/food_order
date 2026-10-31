package applog

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestFlattenAndContext(t *testing.T) {
	obj := map[string]any{}
	flatten(obj, "", slog.Group("http", slog.Int("status", 500), slog.Duration("took", time.Second)))
	flatten(obj, "", slog.Any("err", errors.New("boom")))
	if obj["http.status"] != int64(500) || obj["http.took"] != "1s" || obj["err"] != "boom" {
		t.Fatalf("%#v", obj)
	}

	ctx := With(context.Background(), "user_id", 7)
	ctx = With(ctx, "req_id", "R1")
	if f := Fields(ctx); len(f) != 2 || f[0].Key != "user_id" || f[1].Key != "req_id" {
		t.Fatalf("%v", f)
	}
}

func TestLevels(t *testing.T) {
	h := &handler{shared: &shared{opts: Options{}}}
	if h.Enabled(context.Background(), slog.LevelDebug) || !h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("debug must be off without Additional")
	}
	h.shared.opts.Additional = true
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug must be on with Additional")
	}
	for l, want := range map[slog.Level]string{slog.LevelDebug: "debug", slog.LevelInfo: "info", slog.LevelWarn: "warn", slog.LevelError: "error", slog.LevelError + 4: "error"} {
		if got := levelName(l); got != want {
			t.Fatalf("%v: %s", l, got)
		}
	}
}

// TestPublish needs a NATS server; it runs only with APPLOG_TEST_NATS set.
func TestPublish(t *testing.T) {
	url := getenv("APPLOG_TEST_NATS")
	if url == "" {
		t.Skip("APPLOG_TEST_NATS not set")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("logs.>")
	if err != nil {
		t.Fatal(err)
	}
	l := New(Options{Service: "test", URL: url})
	defer l.Close()
	time.Sleep(200 * time.Millisecond)
	ctx := With(context.Background(), "user_id", 7)
	l.WarnContext(ctx, "hello", "n", 1)
	l.Debug("dropped without Additional")
	l.Logger.With("a", "b").WithGroup("g").Error("bad", "k", "v")
	_ = l.nc.Flush()

	m, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(m.Data, &got)
	if m.Subject != "logs.warn" || got["msg"] != "hello" || got["service"] != "test" || got["user_id"] != float64(7) || got["source"] == nil {
		t.Fatalf("%s %s", m.Subject, m.Data)
	}
	m, err = sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(m.Data, &got)
	if m.Subject != "logs.error" || got["a"] != "b" || got["g.k"] != "v" {
		t.Fatalf("%s %s", m.Subject, m.Data)
	}
}

func getenv(k string) string { return os.Getenv(k) }
