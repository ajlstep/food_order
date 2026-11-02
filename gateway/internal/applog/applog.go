// Package applog is the logger of the service: every record goes to NATS
// on logs.<level> (logs.info, logs.warn, logs.error, logs.debug), where the
// logs service saves it. Nothing is printed to the console, except when
// there is no broker, when a record could not be published, or when
// Console is on.
//
// Debug records (the detailed "additional" logs: every call, every step,
// with its context) are sent only when Additional is on.
//
// Context: With(ctx, "user_id", 7, ...) attaches fields to ctx; every
// record logged with that ctx (log.InfoContext(ctx, ...)) carries them.
//
// The same file is copied in every Go service of food-order.
package applog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Options say where the logs go.
type Options struct {
	Service    string // core, catalog, bot, parser
	URL        string // NATS; empty = console only
	Token      string
	User       string
	Password   string
	Additional bool // also send debug records (the detailed logs)
	Console    bool // also print every record to stderr

	Stream  string        // LOGS: created if it does not exist yet, so nothing is lost before the logs service starts
	Subject string        // logs (records go to <Subject>.<level>)
	MaxAge  time.Duration // max age of the stream when applog creates it
}

// Logger is the service logger plus its connection.
type Logger struct {
	*slog.Logger
	nc *nats.Conn
}

// New makes the logger. It never fails because of the broker: while NATS is
// not reachable the records wait in the client buffer (or go to stderr when
// the buffer is full).
func New(o Options) *Logger {
	if o.Subject == "" {
		o.Subject = "logs"
	}
	if o.Stream == "" {
		o.Stream = "LOGS"
	}
	if o.MaxAge == 0 {
		o.MaxAge = 7 * 24 * time.Hour
	}
	host, _ := os.Hostname()
	h := &handler{shared: &shared{
		opts:    o,
		host:    host,
		console: slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}),
	}}
	l := &Logger{Logger: slog.New(h)}
	if o.URL == "" {
		h.shared.consoleOnly = true
		return l
	}
	opts := []nats.Option{
		nats.Name("food-order-" + o.Service + "-logs"),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectWait(2 * time.Second),
		nats.ReconnectBufSize(16 << 20),
		// the connection's own problems go to stderr: logging them into the
		// connection that is down would only fill the buffer
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				h.shared.toConsole(slog.LevelWarn, "logs: nats disconnected, records are buffered", "err", err)
			}
		}),
		nats.ConnectHandler(func(nc *nats.Conn) { go ensureStream(nc, o, h.shared) }),
		nats.ReconnectHandler(func(nc *nats.Conn) { go ensureStream(nc, o, h.shared) }),
	}
	switch {
	case o.Token != "":
		opts = append(opts, nats.Token(o.Token))
	case o.User != "":
		opts = append(opts, nats.UserInfo(o.User, o.Password))
	}
	nc, err := nats.Connect(o.URL, opts...)
	if err != nil {
		h.shared.consoleOnly = true
		h.shared.toConsole(slog.LevelError, "logs: cannot use nats, logging to the console", "url", o.URL, "err", err)
		return l
	}
	if nc.IsConnected() {
		go ensureStream(nc, o, h.shared)
	}
	h.shared.nc = nc
	l.nc = nc
	return l
}

// Close sends what is still buffered (waits at most 3 s) and closes.
func (l *Logger) Close() {
	if l.nc == nil {
		return
	}
	if l.nc.IsConnected() {
		_ = l.nc.FlushTimeout(3 * time.Second)
	}
	l.nc.Close()
}

// Fatal logs err at error level and also prints it to stderr (the broker may
// be the very thing that is missing), then flushes. Use it right before exit.
func (l *Logger) Fatal(msg string, args ...any) {
	l.Error(msg, args...)
	if h, ok := l.Handler().(*handler); ok && !h.shared.consoleOnly && !h.shared.opts.Console {
		h.shared.toConsole(slog.LevelError, msg, args...)
	}
	l.Close()
}

var streamOnce sync.Map // stream name -> struct{} once it exists

func ensureStream(nc *nats.Conn, o Options, sh *shared) {
	if _, done := streamOnce.Load(o.Stream); done {
		return
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := js.Stream(ctx, o.Stream); err == nil {
		streamOnce.Store(o.Stream, struct{}{})
		return
	}
	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name: o.Stream, Subjects: []string{o.Subject + ".>"}, Storage: jetstream.FileStorage, MaxAge: o.MaxAge,
	})
	if err == nil || errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
		streamOnce.Store(o.Stream, struct{}{})
		return
	}
	sh.toConsole(slog.LevelWarn, "logs: could not create the logs stream (is JetStream on?)", "stream", o.Stream, "err", err)
}

// ------------------------------------------------------------ context

type ctxKey struct{}

// With returns ctx carrying more fields (key, value pairs, like slog).
// Records logged with the returned ctx carry them all.
func With(ctx context.Context, args ...any) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	old, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	r := slog.NewRecord(time.Time{}, 0, "", 0)
	r.Add(args...)
	attrs := make([]slog.Attr, 0, len(old)+r.NumAttrs())
	attrs = append(attrs, old...)
	r.Attrs(func(a slog.Attr) bool { attrs = append(attrs, a); return true })
	return context.WithValue(ctx, ctxKey{}, attrs)
}

// Fields returns the fields attached to ctx with With.
func Fields(ctx context.Context) []slog.Attr {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	return a
}

// Since is a helper for durations in logs: milliseconds with 1 decimal.
func Since(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()/100) / 10
}

// ------------------------------------------------------------ handler

type shared struct {
	opts        Options
	host        string
	nc          *nats.Conn
	consoleOnly bool
	console     slog.Handler
	mu          sync.Mutex // console writes
}

func (s *shared) toConsole(level slog.Level, msg string, args ...any) {
	r := slog.NewRecord(time.Now(), level, msg, 0)
	r.Add(args...)
	r.AddAttrs(slog.String("service", s.opts.Service))
	s.mu.Lock()
	_ = s.console.Handle(context.Background(), r)
	s.mu.Unlock()
}

type handler struct {
	shared *shared
	attrs  []slog.Attr // from Logger.With
	groups []string    // from Logger.WithGroup
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || h.shared.opts.Additional
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.attrs = append(append([]slog.Attr{}, h.attrs...), prefixed(h.groups, as)...)
	return &n
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.groups = append(append([]string{}, h.groups...), name)
	return &n
}

// prefixed turns attrs inside groups into "group.key" attrs.
func prefixed(groups []string, as []slog.Attr) []slog.Attr {
	if len(groups) == 0 {
		return as
	}
	p := strings.Join(groups, ".") + "."
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = slog.Attr{Key: p + a.Key, Value: a.Value}
	}
	return out
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	sh := h.shared
	level := levelName(r.Level)

	obj := map[string]any{}
	put := func(a slog.Attr) { flatten(obj, "", a) }
	for _, a := range Fields(ctx) {
		put(a)
	}
	for _, a := range h.attrs {
		put(a)
	}
	var recAttrs []slog.Attr
	r.Attrs(func(a slog.Attr) bool { recAttrs = append(recAttrs, a); return true })
	for _, a := range prefixed(h.groups, recAttrs) {
		put(a)
	}
	// the fixed fields win over attrs with the same name
	obj["ts"] = r.Time.UTC().Format(time.RFC3339Nano)
	obj["service"] = sh.opts.Service
	obj["msg"] = r.Message
	if sh.host != "" {
		obj["host"] = sh.host
	}
	if r.PC != 0 {
		f, _ := runtime.CallersFrames([]uintptr{r.PC}).Next()
		obj["source"] = fmt.Sprintf("%s:%d", shortFile(f.File), f.Line)
		obj["func"] = shortFunc(f.Function)
	}

	sent := false
	if !sh.consoleOnly && sh.nc != nil {
		if raw, err := json.Marshal(obj); err == nil {
			sent = sh.nc.Publish(sh.opts.Subject+"."+level, raw) == nil
		}
	}
	if !sent || sh.opts.Console {
		c := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		for _, a := range Fields(ctx) {
			c.AddAttrs(a)
		}
		c.AddAttrs(h.attrs...)
		c.AddAttrs(prefixed(h.groups, recAttrs)...)
		sh.mu.Lock()
		_ = sh.console.Handle(ctx, c)
		sh.mu.Unlock()
	}
	return nil
}

// flatten writes one attr into obj; groups become "group.key".
func flatten(obj map[string]any, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	switch a.Value.Kind() {
	case slog.KindGroup:
		p := key
		if a.Key == "" {
			p = prefix
		}
		for _, g := range a.Value.Group() {
			flatten(obj, p, g)
		}
		return
	case slog.KindDuration:
		obj[key] = a.Value.Duration().String()
		return
	case slog.KindTime:
		obj[key] = a.Value.Time().UTC().Format(time.RFC3339Nano)
		return
	}
	v := a.Value.Any()
	switch x := v.(type) {
	case error:
		obj[key] = x.Error()
	case fmt.Stringer:
		obj[key] = x.String()
	case []byte:
		obj[key] = string(x)
	default:
		if _, err := json.Marshal(x); err != nil {
			obj[key] = fmt.Sprint(x)
		} else {
			obj[key] = x
		}
	}
}

func shortFile(path string) string {
	// keep "internal/service/menu.go", not the whole build path
	if i := strings.Index(path, "/internal/"); i >= 0 {
		return path[i+1:]
	}
	return filepath.Base(path)
}

func shortFunc(fn string) string {
	if i := strings.LastIndex(fn, "/"); i >= 0 {
		fn = fn[i+1:]
	}
	return fn
}

// Discard is a logger that drops everything (for tests).
func Discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
