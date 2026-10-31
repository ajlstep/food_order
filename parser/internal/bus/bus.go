// Package bus is the thin layer over NATS JetStream: connect, publish
// events in the common envelope and consume them with a durable consumer.
//
// The stream FOOD (food.>) belongs to core, which creates it at start-up;
// the parser only waits for it. Same code as catalog/internal/bus, except
// where the consumer starts the first time (see Consume).
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

	"food-order/parser/internal/applog"
	"food-order/parser/internal/contracts"
)

// ErrPermanent marks an event that will never succeed (bad JSON, unknown
// type): it is dropped (Term), not redelivered.
var ErrPermanent = errors.New("permanent")

// ErrOff is returned by Publish when the parser runs without a broker.
var ErrOff = errors.New("bus: no broker configured")

// Publisher publishes one event. Implemented by *Bus, Off and test fakes.
type Publisher interface {
	Publish(ctx context.Context, subject string, data any) error
}

// Off is the publisher used when PARSER_NATS_ENABLED=false.
type Off struct{}

// Publish always fails with ErrOff.
func (Off) Publish(context.Context, string, any) error { return ErrOff }

// Config says where to connect and which stream/consumer to use.
type Config struct {
	URL      string
	Token    string
	User     string
	Password string
	Stream   string // FOOD
	Durable  string // parser
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
		nats.Name("food-order-parser"),
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

// StartBack: on its very first start the consumer also takes the requests
// of this last period (see Consume).
const StartBack = time.Hour

// backoff is the pause before each redelivery; MaxDeliver is one more.
var backoff = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// Handler handles one raw event. nil = ack; ErrPermanent = drop; else redeliver.
type Handler func(ctx context.Context, subject string, data []byte) error

// Consume runs the durable consumer on the given subjects until ctx ends.
// While the stream does not exist (core not started yet) it waits.
// onReady is called once the consumer exists; onGiveUp when an event failed
// on its last delivery.
func (b *Bus) Consume(ctx context.Context, subjects []string, h Handler, onReady func(), onGiveUp func(subject string, err error)) error {
	ccfg := jetstream.ConsumerConfig{
		Durable:        b.cfg.Durable,
		FilterSubjects: subjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        time.Minute,
		MaxDeliver:     len(backoff) + 1,
		BackOff:        backoff,
	}
	var cons jetstream.Consumer
	for {
		// Where the consumer starts matters only the first time (a durable
		// consumer then keeps its place, and a request sent while the parser
		// was down is still delivered): it starts StartBack ago, so a request
		// core sent just before the parser's first start is answered, but the
		// week of old requests in the stream does not send the parser to the
		// site for days long gone. An existing consumer keeps its start
		// (JetStream does not allow changing it).
		ccfg.DeliverPolicy = jetstream.DeliverByStartTimePolicy
		start := b.now().Add(-StartBack)
		ccfg.OptStartTime = &start
		if old, err := b.js.Consumer(ctx, b.cfg.Stream, b.cfg.Durable); err == nil {
			ccfg.DeliverPolicy, ccfg.OptStartTime = old.CachedInfo().Config.DeliverPolicy, old.CachedInfo().Config.OptStartTime
		}
		c, err := b.js.CreateOrUpdateConsumer(ctx, b.cfg.Stream, ccfg)
		if err == nil {
			cons = c
			break
		}
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			b.log.Warn("stream not found yet (core creates it), waiting", "stream", b.cfg.Stream)
		} else {
			b.log.Warn("nats consumer not ready, retrying", "err", err)
		}
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
	if onReady != nil {
		onReady()
	}
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
	// 128 bits -> 26 chars of 5 bits: two leading zero bits make it 130
	acc, bits := uint64(0), 2
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
