// Package events publishes the food.* events the same way in every service:
// the common envelope, an id that is also the JetStream message id, so the
// same event sent twice is stored once.
package events

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Version is the version of the envelope.
const Version = 1

// Envelope is the common wrapper of every event.
type Envelope struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Version int             `json:"version"`
	TS      time.Time       `json:"ts"`
	Data    json.RawMessage `json:"data"`
}

// NewEnvelope wraps the data of an event.
func NewEnvelope(subject string, data any, now time.Time) (Envelope, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, fmt.Errorf("events: encode %s: %w", subject, err)
	}
	return Envelope{ID: NewID(now), Type: subject, Version: Version, TS: now, Data: raw}, nil
}

// Publisher sends one event. Services depend on this, so tests can record
// what would have been sent.
type Publisher interface {
	Publish(ctx context.Context, subject string, data any) error
}

// publishTimeout keeps a broker that does not answer from holding up a request.
const publishTimeout = 5 * time.Second

// JetStreamPublisher publishes to the streams.
type JetStreamPublisher struct {
	js  jetstream.JetStream
	log *slog.Logger
	now func() time.Time
}

// NewPublisher makes a publisher on an open connection.
func NewPublisher(nc *nats.Conn, log *slog.Logger) (*JetStreamPublisher, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	return &JetStreamPublisher{js: js, log: log, now: time.Now}, nil
}

// Publish wraps data in the envelope and waits for the stream to confirm it.
func (p *JetStreamPublisher) Publish(ctx context.Context, subject string, data any) error {
	env, err := NewEnvelope(subject, data, p.now())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("events: encode %s: %w", subject, err)
	}
	pctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	if _, err := p.js.Publish(pctx, subject, raw, jetstream.WithMsgID(env.ID)); err != nil {
		return fmt.Errorf("events: publish %s: %w", subject, err)
	}
	p.log.DebugContext(ctx, "event published", "subject", subject, "event_id", env.ID, "bytes", len(raw))
	return nil
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID (26 characters, sortable by time): 48 bits of
// milliseconds and 80 random bits, in Crockford base32.
func NewID(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	_, _ = rand.Read(b[6:]) // never fails on the systems Go supports

	var out strings.Builder
	out.Grow(26)
	acc, bits := uint64(0), 2 // two leading zero bits: 130 bits make 26 characters
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
