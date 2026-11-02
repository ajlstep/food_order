// Package streams is the one place that knows which JetStream streams exist
// in food-order, and what they hold. Services do not create streams: the
// nats-setup job runs Ensure once, with the admin user. Services only use them.
//
// Request/reply messages (rpc.>) are deliberately not here, see package rpc.
package streams

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Names of the streams.
const (
	Food   = "FOOD"   // events between services: food.>
	Logs   = "LOGS"   // logs of all services: logs.>
	Notify = "NOTIFY" // messages waiting to be shown to a person: notify.>
)

// Retention of messages that nobody took yet.
const keep = 7 * 24 * time.Hour

// Durable consumers created by Ensure. A service gets permission to read only
// its own consumer, and cannot create others (see nuts/nats.conf).
const (
	ConsumerBotTelegram = "bot-telegram" // the bot reads notify.telegram
)

// Ensure creates the streams and consumers, or brings them to this
// configuration if they exist. It can run as many times as needed.
func Ensure(ctx context.Context, js jetstream.JetStream) error {
	for _, cfg := range []jetstream.StreamConfig{
		{Name: Food, Subjects: []string{"food.>"}},
		{Name: Logs, Subjects: []string{"logs.>"}},
		{Name: Notify, Subjects: []string{"notify.>"}},
	} {
		cfg.Storage = jetstream.FileStorage
		cfg.Retention = jetstream.LimitsPolicy
		cfg.MaxAge = keep
		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("streams: %s: %w", cfg.Name, err)
		}
	}

	_, err := js.CreateOrUpdateConsumer(ctx, Notify, jetstream.ConsumerConfig{
		Durable:       ConsumerBotTelegram,
		FilterSubject: "notify.telegram",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		DeliverPolicy: jetstream.DeliverAllPolicy, // nothing is lost while the bot is down
	})
	if err != nil {
		return fmt.Errorf("streams: consumer %s: %w", ConsumerBotTelegram, err)
	}
	return nil
}

// ErrNotReady is returned by WaitAndEnsure when the broker did not come up in time.
var ErrNotReady = errors.New("streams: nats is not ready")

// WaitAndEnsure runs Ensure, retrying until the broker is ready or ctx ends.
func WaitAndEnsure(ctx context.Context, js jetstream.JetStream, retryEvery time.Duration, onRetry func(error)) error {
	for {
		err := Ensure(ctx, js)
		if err == nil {
			return nil
		}
		if onRetry != nil {
			onRetry(err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %v", ErrNotReady, err)
		case <-time.After(retryEvery):
		}
	}
}
