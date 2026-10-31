// Package collector listens to logs.> on NATS JetStream and saves every
// message in the database.
package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"food-order/logs/internal/config"
	"food-order/logs/internal/store"
)

// Run connects to NATS, creates the stream and the durable consumer, and
// saves messages until ctx ends. It also deletes old rows (cfg.Keep).
func Run(ctx context.Context, cfg config.Config, st *store.Store, log *slog.Logger) error {
	opts := []nats.Option{
		nats.Name("food-order-logs"),
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
	case cfg.NATSToken != "":
		opts = append(opts, nats.Token(cfg.NATSToken))
	case cfg.NATSUser != "":
		opts = append(opts, nats.UserInfo(cfg.NATSUser, cfg.NATSPassword))
	}
	nc, err := nats.Connect(cfg.NATSURL, opts...)
	if err != nil {
		return fmt.Errorf("collector: %w", err)
	}
	defer nc.Drain()
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("collector: %w", err)
	}

	cons, err := setup(ctx, js, cfg, log)
	if err != nil {
		return err
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		at := time.Now()
		var seq uint64
		if md, err := msg.Metadata(); err == nil {
			at, seq = md.Timestamp, md.Sequence.Stream // when NATS got it, not when we read it
		}
		e := Parse(msg.Subject(), msg.Data(), at)
		e.Seq = seq
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := st.Add(wctx, e); err != nil {
			log.Error("could not save log, it will be redelivered", "subject", msg.Subject(), "err", err)
			_ = msg.NakWithDelay(5 * time.Second)
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("collector: consume: %w", err)
	}
	defer cc.Stop()
	log.Info("collecting logs", "stream", cfg.Stream, "subject", cfg.Subject, "durable", cfg.Durable)

	if cfg.Keep > 0 {
		go cleanup(ctx, st, cfg.Keep, log)
	}
	<-ctx.Done()
	return nil
}

// setup creates the stream and consumer, waiting for the broker if needed.
func setup(ctx context.Context, js jetstream.JetStream, cfg config.Config, log *slog.Logger) (jetstream.Consumer, error) {
	for {
		_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     cfg.Stream,
			Subjects: []string{cfg.Subject},
			Storage:  jetstream.FileStorage,
			MaxAge:   cfg.MaxAge,
		})
		if err == nil {
			var cons jetstream.Consumer
			cons, err = js.CreateOrUpdateConsumer(ctx, cfg.Stream, jetstream.ConsumerConfig{
				Durable:       cfg.Durable,
				AckPolicy:     jetstream.AckExplicitPolicy,
				AckWait:       30 * time.Second,
				DeliverPolicy: jetstream.DeliverAllPolicy, // logs sent while the collector was down are not lost
			})
			if err == nil {
				return cons, nil
			}
		}
		log.Warn("nats not ready, retrying", "err", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func cleanup(ctx context.Context, st *store.Store, keep time.Duration, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		n, err := st.DeleteBefore(ctx, time.Now().Add(-keep))
		switch {
		case errors.Is(err, context.Canceled):
			return
		case err != nil:
			log.Warn("cleanup failed", "err", err)
		case n > 0:
			log.Info("old logs deleted", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
