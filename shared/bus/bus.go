// Package bus opens the NATS connection the same way in every service.
package bus

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// Options say how to connect.
type Options struct {
	Name     string // shown in the NATS monitor, for example "users"
	URL      string
	User     string
	Password string
}

// Connect connects and keeps trying to reconnect when the broker goes away.
// The first connection must work: a service that cannot reach NATS has
// nothing to do, so it is better to stop and let Docker restart it.
func Connect(o Options, log *slog.Logger) (*nats.Conn, error) {
	opts := []nats.Option{
		nats.Name("food-order-" + o.Name),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.Timeout(5 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("nats disconnected", "err", err)
			}
		}),
		nats.ReconnectHandler(func(*nats.Conn) { log.Info("nats reconnected") }),
		// The server tells us when we try something we may not do.
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			subject := ""
			if sub != nil {
				subject = sub.Subject
			}
			log.Error("nats error", "subject", subject, "err", err)
		}),
	}
	if o.User != "" {
		opts = append(opts, nats.UserInfo(o.User, o.Password))
	}
	nc, err := nats.Connect(o.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("bus: connect to %s: %w", o.URL, err)
	}
	return nc, nil
}
