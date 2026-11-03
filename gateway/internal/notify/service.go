// Package notify turns the events meant for a person into messages in the
// queue of a channel. It decides who gets what and on which channel; it does
// not know how a message looks on Telegram, that is the job of the reader of
// the queue (the bot).
//
// It starts in the gateway because the gateway already talks to users; it
// can move to a service of its own without changing the queues.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/notify"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
	"food-order/shared/rpc"
	"food-order/shared/streams"
)

// rule says what an event becomes. A new kind of notification is one row.
type rule struct {
	kind  string
	class notify.Class
}

var rules = map[string]rule{
	food.SubjectRecommendationReady: {notify.KindRecommendation, notify.ClassNormal},
	food.SubjectRatingRequested:     {notify.KindRating, notify.ClassNormal},
	// somebody waits for an answer: on every channel, whatever the settings
	food.SubjectOrgInvite: {notify.KindOrgInvite, notify.ClassImportant},
	food.SubjectOrgNotice: {notify.KindOrgNotice, notify.ClassImportant},
}

// Directory says how to reach a person.
type Directory interface {
	Channels(ctx context.Context, requestID, userID string) (users.ChannelsResponse, error)
	Settings(ctx context.Context, requestID, userID string) (map[string]string, error)
}

// Queue holds the messages of the channels until somebody reads them.
type Queue interface {
	Put(ctx context.Context, channel string, m notify.Message) error
}

// Service applies the rules.
type Service struct {
	directory Directory
	queue     Queue
	log       *slog.Logger
}

// NewService makes a Service from its parts (tests give fake ones).
func NewService(d Directory, q Queue, log *slog.Logger) *Service {
	return &Service{directory: d, queue: q, log: log}
}

// Handle works on one raw event. It answers the way streams.Consume expects:
// nil when there is nothing more to do, streams.ErrDrop when the event can
// never work, another error when it is worth trying again.
func (s *Service) Handle(ctx context.Context, raw []byte) error {
	var ev events.Envelope
	if err := json.Unmarshal(raw, &ev); err != nil {
		return fmt.Errorf("%w: the event is not valid: %v", streams.ErrDrop, err)
	}
	if ev.Type == food.SubjectAlert {
		return s.alert(ctx, ev)
	}
	r, ok := rules[ev.Type]
	if !ok {
		return fmt.Errorf("%w: no rule for %q", streams.ErrDrop, ev.Type)
	}
	userID, err := personOf(ev)
	if err != nil {
		return err
	}
	return s.deliver(ctx, ev, r, userID)
}

// alert goes to the admin chat. The bot knows which one, so no ChatRef.
func (s *Service) alert(ctx context.Context, ev events.Envelope) error {
	m := notify.Message{ID: messageID(ev, notify.ChannelTelegram), Kind: notify.KindAlert, Data: ev.Data}
	return s.queue.Put(ctx, notify.ChannelTelegram, m)
}

// personOf reads user_id from the data of the event.
func personOf(ev events.Envelope) (string, error) {
	var d struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		return "", fmt.Errorf("%w: %s: user_id: %v", streams.ErrDrop, ev.Type, err)
	}
	if d.UserID == "" {
		return "", fmt.Errorf("%w: %s has no user_id", streams.ErrDrop, ev.Type)
	}
	return d.UserID, nil
}

func (s *Service) deliver(ctx context.Context, ev events.Envelope, r rule, userID string) error {
	channels, err := s.directory.Channels(ctx, ev.ID, userID)
	if err != nil {
		return s.lookupFailed(ctx, ev, userID, err)
	}
	targets := chooseChannels(channels, r.class)
	if len(targets) == 0 {
		s.log.InfoContext(ctx, "no active channel, nothing sent", "event", ev.Type, "user_id", userID)
		return nil
	}
	if r.class == notify.ClassNormal {
		on, err := s.personalOn(ctx, ev.ID, userID)
		if err != nil {
			return s.lookupFailed(ctx, ev, userID, err)
		}
		if !on {
			s.log.InfoContext(ctx, "personal notifications are off, nothing sent", "event", ev.Type, "user_id", userID)
			return nil
		}
	}
	for _, c := range targets {
		m := notify.Message{ID: messageID(ev, c.Channel), Kind: r.kind, UserID: userID, ChatRef: c.ChatRef, Data: ev.Data}
		if err := s.queue.Put(ctx, c.Channel, m); err != nil {
			return err // try again; the same message id keeps it from doubling
		}
	}
	s.log.DebugContext(ctx, "notification queued", "event", ev.Type, "user_id", userID, "channels", len(targets))
	return nil
}

// chooseChannels applies the class of a notification.
func chooseChannels(c users.ChannelsResponse, class notify.Class) []users.Channel {
	var out []users.Channel
	for _, ch := range c.Channels {
		if ch.Status != users.IdentityActive || ch.ChatRef == "" {
			continue
		}
		if class == notify.ClassImportant || ch.Channel == c.Latest {
			out = append(out, ch)
		}
	}
	return out
}

func (s *Service) personalOn(ctx context.Context, requestID, userID string) (bool, error) {
	settings, err := s.directory.Settings(ctx, requestID, userID)
	if err != nil {
		return false, err
	}
	return settings[users.SettingNotifyPersonal] != "false", nil
}

// lookupFailed separates "users refused" from "users did not answer". After
// a refusal (no such account, blocked) the event is done; a failure of users
// is worth another try.
func (s *Service) lookupFailed(ctx context.Context, ev events.Envelope, userID string, err error) error {
	var re *rpc.Error
	if errors.As(err, &re) && !temporary(re.Code) {
		s.log.InfoContext(ctx, "nobody to notify", "event", ev.Type, "user_id", userID, "code", re.Code)
		return nil
	}
	return fmt.Errorf("looking up the person of %s: %w", ev.Type, err)
}

func temporary(code string) bool {
	switch code {
	case rpc.CodeInternal, rpc.CodeUnavailable, rpc.CodeTimeout:
		return true
	}
	return false
}

// messageID makes the id NATS uses to ignore a second copy. The event id is
// not enough: one event can go on several channels.
func messageID(ev events.Envelope, channel string) string {
	if ev.ID == "" {
		return ""
	}
	return ev.ID + "." + channel
}
