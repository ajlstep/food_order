package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"food-order/shared/contracts/notify"
	"food-order/shared/contracts/users"
)

// Caller sends a request to a service (rpc.Client does it).
type Caller interface {
	CallFor(ctx context.Context, subject, requestID, userID string, in, out any) error
}

// New builds a Service on the real parts: users over rpc, the queue in NATS.
func New(nc *nats.Conn, caller Caller, timeout time.Duration, log *slog.Logger) (*Service, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("notify: %w", err)
	}
	return NewService(usersDirectory{caller, timeout}, streamQueue{js}, log), nil
}

// usersDirectory asks the users service.
type usersDirectory struct {
	caller  Caller
	timeout time.Duration
}

func (d usersDirectory) Channels(ctx context.Context, requestID, userID string) (users.ChannelsResponse, error) {
	var out users.ChannelsResponse
	err := d.call(ctx, users.SubjectProfileChannelsGet, requestID, userID, &out)
	return out, err
}

func (d usersDirectory) Settings(ctx context.Context, requestID, userID string) (map[string]string, error) {
	var out users.SettingsResponse
	err := d.call(ctx, users.SubjectProfileSettingsGet, requestID, userID, &out)
	return out.Settings, err
}

func (d usersDirectory) call(ctx context.Context, subject, requestID, userID string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	return d.caller.CallFor(ctx, subject, requestID, userID, nil, out)
}

// streamQueue writes to the stream NOTIFY.
type streamQueue struct{ js jetstream.JetStream }

func (q streamQueue) Put(ctx context.Context, channel string, m notify.Message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("notify: encode message: %w", err)
	}
	var opts []jetstream.PublishOpt
	if m.ID != "" {
		opts = append(opts, jetstream.WithMsgID(m.ID))
	}
	if _, err := q.js.Publish(ctx, notify.Subject(channel), data, opts...); err != nil {
		return fmt.Errorf("notify: put in %s: %w", channel, err)
	}
	return nil
}
