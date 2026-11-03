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

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/notify"
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
	ConsumerNotifier    = "notifier"     // the gateway reads the events meant for people
	ConsumerCore        = "core"
	ConsumerCatalog     = "catalog"
	ConsumerParser      = "parser"
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

	consumers := []struct {
		stream string
		cfg    jetstream.ConsumerConfig
	}{
		{Notify, jetstream.ConsumerConfig{
			Durable:       ConsumerBotTelegram,
			FilterSubject: notify.Subject(notify.ChannelTelegram),
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       30 * time.Second,
			DeliverPolicy: jetstream.DeliverAllPolicy, // nothing is lost while the bot is down
		}},
		{Food, jetstream.ConsumerConfig{
			Durable:        ConsumerNotifier,
			FilterSubjects: notify.Events,
			AckPolicy:      jetstream.AckExplicitPolicy,
			AckWait:        30 * time.Second,
			DeliverPolicy:  jetstream.DeliverNewPolicy, // the first time: no old alerts
		}},
		{Food, foodConsumer(ConsumerCore, 30*time.Second, food.SubjectMenuReady, food.SubjectMenuError,
			food.SubjectRecommendationGenerated, food.SubjectFeedbackParsed)},
		{Food, foodConsumer(ConsumerCatalog, 30*time.Second, food.SubjectMenuFetched, food.SubjectDishAnalyzed, food.SubjectDishEmbedded)},
		{Food, foodConsumer(ConsumerParser, ParserTimeout+30*time.Second, food.SubjectMenuRequested)},
	}
	for _, c := range consumers {
		if _, err := js.CreateOrUpdateConsumer(ctx, c.stream, c.cfg); err != nil {
			return fmt.Errorf("streams: consumer %s: %w", c.cfg.Durable, err)
		}
	}
	return nil
}

// foodConsumer is the consumer of a service on FOOD. Everything is delivered:
// an answer to a request of the service must not be lost.
func foodConsumer(name string, ackWait time.Duration, subjects ...string) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		Durable:        name,
		FilterSubjects: subjects,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        ackWait,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	}
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
