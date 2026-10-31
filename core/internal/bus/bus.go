// Package bus is the thin layer over NATS JetStream: connect, make sure the
// stream exists, publish events in the common envelope and consume them
// with a durable consumer.
//
// Core owns the stream FOOD (food.>): it creates it at start-up. The other
// services (bot, ...) wait for it.
package bus

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"food-order/core/internal/applog"
	"food-order/core/internal/contracts"
)

// ErrPermanent marks an event that will never succeed (bad JSON, unknown
// type): it is dropped (Term), not redelivered.
var ErrPermanent = errors.New("permanent")

// ErrOff is returned by Publish when core runs without a broker.
var ErrOff = errors.New("bus: no broker configured")

// Publisher publishes one event. Implemented by *Bus, Off and test fakes.
type Publisher interface {
	Publish(ctx context.Context, subject string, data any) error
}

// Off is the publisher used when CORE_NATS_URL is empty.
type Off struct{}

// Publish always fails with ErrOff.
func (Off) Publish(context.Context, string, any) error { return ErrOff }

// Config says where to connect and which stream/consumer to use.
type Config struct {
	URL      string
	Token    string
	User     string
	Password string
	Stream   string        // FOOD
	Durable  string        // core
	MaxAge   time.Duration // how long the stream keeps messages
}

// Bus is a connection to NATS JetStream.
type Bus struct {
	nc  *nats.Conn
	js  jetstream.JetStream
	cfg Config
	log *slog.Logger
	now func() time.Time
}

// Connect connects to NATS. It does not wait for the server: the client
// keeps reconnecting in the background and calls fail until it is there.
func Connect(cfg Config, log *slog.Logger) (*Bus, error) {
	opts := []nats.Option{
		nats.Name("food-order-core"),
		nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("nats disconnected", "err", err)
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) { log.Info("nats reconnected") }),
	}
	switch {
	case cfg.Token != "":
		opts = append(opts, nats.Token(cfg.Token))
	case cfg.User != "":
		opts = append(opts, nats.UserInfo(cfg.User, cfg.Password))
	}
	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("bus: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("bus: %w", err)
	}
	return &Bus{nc: nc, js: js, cfg: cfg, log: log, now: time.Now}, nil
}

// Close drains the connection.
func (b *Bus) Close() { _ = b.nc.Drain() }

// EnsureStream creates (or updates) the stream FOOD on food.>, retrying
// until the server answers or ctx ends.
func (b *Bus) EnsureStream(ctx context.Context) error {
	scfg := jetstream.StreamConfig{
		Name:       b.cfg.Stream,
		Subjects:   []string{"food.>"},
		Storage:    jetstream.FileStorage,
		Retention:  jetstream.LimitsPolicy,
		MaxAge:     b.cfg.MaxAge,
		Duplicates: 2 * time.Minute, // the same envelope id twice in 2 min is stored once
	}
	for {
		_, err := b.js.CreateOrUpdateStream(ctx, scfg)
		if err == nil {
			b.log.Info("nats stream ready", "stream", scfg.Name, "subjects", scfg.Subjects, "max_age", scfg.MaxAge)
			return nil
		}
		b.log.Warn("nats stream not ready, retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// Publish wraps data in the common envelope and publishes it to JetStream.
// The envelope id is also the JetStream message id (dedup).
func (b *Bus) Publish(ctx context.Context, subject string, data any) error {
	env, err := NewEnvelope(subject, data, b.now())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	start := time.Now()
	ack, err := b.js.Publish(pctx, subject, raw, jetstream.WithMsgID(env.ID))
	if err != nil {
		b.log.WarnContext(ctx, "event publish failed", "subject", subject, "event_id", env.ID, "bytes", len(raw), "ms", applog.Since(start), "err", err)
		return fmt.Errorf("bus: publish %s: %w", subject, err)
	}
	b.log.DebugContext(ctx, "event published", "subject", subject, "event_id", env.ID, "bytes", len(raw),
		"stream_seq", ack.Sequence, "duplicate", ack.Duplicate, "ms", applog.Since(start), "data", json.RawMessage(env.Data))
	return nil
}

// NewEnvelope makes the common wrapper of an event.
func NewEnvelope(subject string, data any, now time.Time) (contracts.Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return contracts.Envelope{}, err
	}
	return contracts.Envelope{ID: NewID(now), Type: subject, Version: 1, TS: now, Data: raw}, nil
}

// backoff is the pause before each redelivery; MaxDeliver is one more.
var backoff = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// Handler handles one raw event. nil = ack; ErrPermanent = drop; else redeliver.
type Handler func(ctx context.Context, subject string, data []byte) error

// Consume runs the durable consumer on the given subjects until ctx ends.
// onGiveUp is called when an event failed on its last delivery.
func (b *Bus) Consume(ctx context.Context, subjects []string, h Handler, onGiveUp func(subject string, err error)) error {
	ccfg := jetstream.ConsumerConfig{
		Durable:        b.cfg.Durable,
		FilterSubjects: subjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        time.Minute,
		MaxDeliver:     len(backoff) + 1,
		BackOff:        backoff,
		DeliverPolicy:  jetstream.DeliverAllPolicy, // answers to our requests must not be lost
	}
	var cons jetstream.Consumer
	for {
		c, err := b.js.CreateOrUpdateConsumer(ctx, b.cfg.Stream, ccfg)
		if err == nil {
			cons = c
			break
		}
		b.log.Warn("nats consumer not ready, retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		hctx, cancel := context.WithTimeout(ctx, 50*time.Second)
		defer cancel()
		hctx = eventContext(hctx, msg)
		start := time.Now()
		b.log.DebugContext(hctx, "event received", "bytes", len(msg.Data()), "data", rawOrText(msg.Data()))
		err := h(hctx, msg.Subject(), msg.Data())
		switch {
		case err == nil:
			b.log.DebugContext(hctx, "event handled", "ms", applog.Since(start))
			_ = msg.Ack()
		case errors.Is(err, ErrPermanent):
			b.log.ErrorContext(hctx, "dropping event", "err", err, "ms", applog.Since(start), "data", rawOrText(msg.Data()))
			_ = msg.Term()
		default:
			n := uint64(0)
			if md, mdErr := msg.Metadata(); mdErr == nil {
				n = md.NumDelivered
			}
			if int(n) >= ccfg.MaxDeliver {
				b.log.ErrorContext(hctx, "event failed on its last delivery", "attempt", n, "err", err, "data", rawOrText(msg.Data()))
				if onGiveUp != nil {
					onGiveUp(msg.Subject(), err)
				}
				_ = msg.Term()
				return
			}
			b.log.WarnContext(hctx, "event failed, it will be redelivered", "attempt", n, "err", err, "ms", applog.Since(start))
			_ = msg.Nak() // the consumer BackOff decides the pause
		}
	})
	if err != nil {
		return fmt.Errorf("bus: consume: %w", err)
	}
	defer cc.Stop()
	b.log.Info("listening for events", "stream", b.cfg.Stream, "durable", b.cfg.Durable, "subjects", subjects)
	<-ctx.Done()
	return nil
}

// eventContext attaches the event to ctx, so every log of its handling
// says which event it was about.
func eventContext(ctx context.Context, msg jetstream.Msg) context.Context {
	var env struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	_ = json.Unmarshal(msg.Data(), &env)
	args := []any{"subject", msg.Subject(), "event_id", env.ID}
	if md, err := msg.Metadata(); err == nil {
		args = append(args, "stream_seq", md.Sequence.Stream, "delivery", md.NumDelivered, "pending", md.NumPending)
	}
	return applog.With(ctx, args...)
}

// rawOrText keeps valid JSON as JSON in the log, anything else as text.
func rawOrText(b []byte) any {
	if len(b) > 16<<10 {
		return string(b[:16<<10]) + "…"
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	return string(b)
}

// ---------------------------------------------------------------- ids

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var idMu sync.Mutex

// NewID returns a ULID (26 chars, sortable by time): 48 bits of
// milliseconds and 80 random bits, in Crockford base32.
func NewID(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	idMu.Lock()
	_, _ = rand.Read(b[6:])
	idMu.Unlock()

	var out strings.Builder
	out.Grow(26)
	// 128 bits -> 26 chars of 5 bits (the first char carries 3 bits)
	var acc uint64
	bits := 0
	// first 2 bits padding: treat as 130 bits with two leading zeros
	acc, bits = 0, 2
	for _, x := range b {
		acc = acc<<8 | uint64(x)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out.WriteByte(crockford[(acc>>uint(bits))&31])
		}
	}
	return out.String()
}
