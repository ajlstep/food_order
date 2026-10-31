package events

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"food-order/bot/internal/applog"
	"food-order/bot/internal/contracts"
)

// Subjects the bot's durable consumer is filtered on.
var Subjects = []string{
	contracts.SubjectRecommendationReady,
	contracts.SubjectRatingRequested,
	contracts.SubjectAlert,
}

// ConsumerConfig says where to connect and which stream/consumer to use.
type ConsumerConfig struct {
	URL      string
	Token    string
	User     string
	Password string
	Stream   string // FOOD
	Durable  string // bot
}

// backoff is the pause before each redelivery; MaxDeliver must be larger.
var backoff = []time.Duration{5 * time.Second, 30 * time.Second, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// Run connects to NATS and handles events until ctx is cancelled.
// If the stream does not exist yet (core has not created it), it waits and retries.
func Run(ctx context.Context, cfg ConsumerConfig, h *Handler, log *slog.Logger) error {
	opts := []nats.Option{
		nats.Name("food-order-bot"),
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
		return err
	}
	defer nc.Drain()

	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}

	cons, err := waitConsumer(ctx, js, cfg, log)
	if err != nil {
		return err
	}

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		handleMsg(ctx, h, msg, log)
	})
	if err != nil {
		return err
	}
	defer cc.Stop()
	log.Info("listening for events", "stream", cfg.Stream, "durable", cfg.Durable, "subjects", Subjects)

	<-ctx.Done()
	return nil
}

func waitConsumer(ctx context.Context, js jetstream.JetStream, cfg ConsumerConfig, log *slog.Logger) (jetstream.Consumer, error) {
	ccfg := jetstream.ConsumerConfig{
		Durable:        cfg.Durable,
		FilterSubjects: Subjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        30 * time.Second,
		MaxDeliver:     len(backoff) + 1,
		BackOff:        backoff,
		DeliverPolicy:  jetstream.DeliverNewPolicy, // the first time: don't replay old alerts
	}
	for {
		cons, err := js.CreateOrUpdateConsumer(ctx, cfg.Stream, ccfg)
		if err == nil {
			return cons, nil
		}
		if !errors.Is(err, jetstream.ErrStreamNotFound) {
			log.Warn("nats consumer not ready, retrying", "err", err)
		} else {
			log.Warn("stream not found yet, waiting for it", "stream", cfg.Stream)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
}

func handleMsg(ctx context.Context, h *Handler, msg jetstream.Msg, log *slog.Logger) {
	hctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	hctx = eventContext(hctx, msg)
	start := time.Now()
	log.DebugContext(hctx, "event received", "bytes", len(msg.Data()), "data", rawOrText(msg.Data()))

	err := h.Handle(hctx, msg.Data())
	switch {
	case err == nil:
		log.DebugContext(hctx, "event handled", "ms", applog.Since(start))
		_ = msg.Ack()
	case errors.Is(err, ErrPermanent):
		log.ErrorContext(hctx, "dropping event", "err", err, "ms", applog.Since(start), "data", rawOrText(msg.Data()))
		_ = msg.Term()
	default:
		n := uint64(0)
		if md, mdErr := msg.Metadata(); mdErr == nil {
			n = md.NumDelivered
		}
		if int(n) >= len(backoff)+1 {
			log.ErrorContext(hctx, "event failed on its last delivery", "attempt", n, "err", err, "data", rawOrText(msg.Data()))
		} else {
			log.WarnContext(hctx, "event failed, it will be redelivered", "attempt", n, "err", err, "ms", applog.Since(start))
		}
		_ = msg.Nak() // the consumer BackOff decides the pause
	}
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
