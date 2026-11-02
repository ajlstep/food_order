// Command natssetup creates the JetStream streams and consumers of food-order
// (see package streams). It runs once when the system starts, with the admin
// user, and exits. Services then connect with their own, limited, users.
//
// Settings (environment): NATS_URL, NATS_USER, NATS_PASSWORD, SETUP_TIMEOUT.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"food-order/shared/bus"
	"food-order/shared/streams"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("nats setup failed", "err", err)
		os.Exit(1)
	}
	log.Info("nats streams are ready")
}

func run(log *slog.Logger) error {
	timeout, err := time.ParseDuration(env("SETUP_TIMEOUT", "60s"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	nc, err := connectWithRetry(ctx, log)
	if err != nil {
		return err
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	return streams.WaitAndEnsure(ctx, js, 2*time.Second, func(err error) {
		log.Warn("streams not ready, retrying", "err", err)
	})
}

// connectWithRetry waits for the broker, which may still be starting.
func connectWithRetry(ctx context.Context, log *slog.Logger) (*nats.Conn, error) {
	opts := bus.Options{
		Name:     "setup",
		URL:      env("NATS_URL", "nats://nats:4222"),
		User:     env("NATS_USER", "admin"),
		Password: env("NATS_PASSWORD", ""),
	}
	for {
		nc, err := bus.Connect(opts, log)
		if err == nil {
			return nc, nil
		}
		log.Warn("nats not reachable, retrying", "err", err)
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(2 * time.Second):
		}
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
