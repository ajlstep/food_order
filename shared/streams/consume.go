package streams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ErrDrop marks a message that will never work (bad shape, nobody to send it
// to): Consume removes it instead of trying again.
var ErrDrop = errors.New("drop the message")

// Handler works on one message. nil means done; an error wrapping ErrDrop
// means "give up on it"; any other error means "try again later".
type Handler func(ctx context.Context, data []byte) error

// handlerTimeout is below AckWait, so a slow message is not delivered twice.
const handlerTimeout = 25 * time.Second

// ParserTimeout: the parser goes to a site, which can be slow.
const ParserTimeout = 2 * time.Minute

// waitForConsumer: how often to look for a consumer that is not there yet
// (nats-setup may still be running).
const waitForConsumer = 5 * time.Second

// retryDelays are the pauses before each new try; the last one repeats.
var retryDelays = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// RetryDelay is the pause after the delivered-th delivery failed (1 = first).
func RetryDelay(delivered uint64) time.Duration {
	i := int(delivered) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(retryDelays) {
		i = len(retryDelays) - 1
	}
	return retryDelays[i]
}

// stuckAfter is the delivery at which a message counts as stuck: it keeps
// coming back (until the stream drops it), but somebody should know.
const stuckAfter = 6

// Option changes how Consume works.
type Option func(*consumeOptions)

type consumeOptions struct {
	onStuck func(subject string, err error)
	timeout time.Duration
}

// Timeout changes how long one message may take. It must stay below the
// AckWait of the consumer, or the message is delivered twice.
func Timeout(d time.Duration) Option {
	return func(o *consumeOptions) { o.timeout = d }
}

// OnStuck calls f once, when a message failed stuckAfter times.
func OnStuck(f func(subject string, err error)) Option {
	return func(o *consumeOptions) { o.onStuck = f }
}

// Consume gives the messages of a durable consumer to h until ctx ends. The
// consumer must already exist: services may not create consumers, see Ensure.
func Consume(ctx context.Context, nc *nats.Conn, stream, consumer string, h Handler, log *slog.Logger, opts ...Option) error {
	o := consumeOptions{timeout: handlerTimeout}
	for _, opt := range opts {
		opt(&o)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("streams: %w", err)
	}
	cons, err := findConsumer(ctx, js, stream, consumer, log)
	if errors.Is(err, context.Canceled) {
		return nil // stopped while waiting: not a failure
	}
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) { handle(ctx, msg, h, o, log) })
	if err != nil {
		return fmt.Errorf("streams: consume %s: %w", consumer, err)
	}
	defer cc.Stop()
	log.Info("reading the queue", "stream", stream, "consumer", consumer)
	<-ctx.Done()
	return nil
}

func findConsumer(ctx context.Context, js jetstream.JetStream, stream, consumer string, log *slog.Logger) (jetstream.Consumer, error) {
	for {
		cons, err := js.Consumer(ctx, stream, consumer)
		if err == nil {
			return cons, nil
		}
		log.Warn("the consumer is not ready, waiting", "stream", stream, "consumer", consumer, "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(waitForConsumer):
		}
	}
}

func handle(ctx context.Context, msg jetstream.Msg, h Handler, o consumeOptions, log *slog.Logger) {
	hctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()

	delivered := uint64(1)
	if md, err := msg.Metadata(); err == nil {
		delivered = md.NumDelivered
	}
	err := h(hctx, msg.Data())
	switch {
	case err == nil:
		_ = msg.Ack()
	case errors.Is(err, ErrDrop):
		log.Error("message dropped", "subject", msg.Subject(), "err", err)
		_ = msg.Term()
	default:
		// Without a delay a Nak comes back at once and would spin.
		if delivered == stuckAfter && o.onStuck != nil {
			o.onStuck(msg.Subject(), err)
		}
		delay := RetryDelay(delivered)
		log.Warn("message failed, it will come back", "subject", msg.Subject(), "delivery", delivered, "in", delay, "err", err)
		_ = msg.NakWithDelay(delay)
	}
}
