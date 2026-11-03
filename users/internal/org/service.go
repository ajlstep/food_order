package org

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
	"food-order/users/internal/profile"
	"food-order/users/internal/refusal"
)

// Limits, so that nobody fills the database through the bot.
const (
	maxNameLen = 64
	maxAddrLen = 200
	maxNoteLen = 200
	inviteTTL  = 7 * 24 * time.Hour
)

var fail = refusal.New

// Service holds the rules of companies, groups and invitations.
type Service struct {
	store Store
	pub   events.Publisher
	log   *slog.Logger
	now   func() time.Time
}

// NewService makes the service. now is the clock (time.Now in production).
func NewService(store Store, pub events.Publisher, log *slog.Logger, now func() time.Time) *Service {
	return &Service{store: store, pub: pub, log: log, now: now}
}

// active refuses anybody who has not confirmed the email, or is blocked.
func (s *Service) active(ctx context.Context, q PeopleStore, userID string) error {
	status, err := q.AccountStatus(ctx, userID)
	switch {
	case errors.Is(err, ErrNotFound):
		return fail("account_not_found")
	case err != nil:
		return fmt.Errorf("load account: %w", err)
	case status == statusActive:
		return nil
	case status == profile.StatusPending:
		return fail(users.ErrEmailNotConfirmed)
	}
	return fail(users.ErrAccountBlocked)
}

// outbox is what must be said to people once the transaction is saved.
type outbox struct {
	invites []food.OrgInvite
	notices []food.OrgNotice
}

func (o *outbox) notice(userID, notice, kind, name string) {
	o.notices = append(o.notices, food.OrgNotice{UserID: userID, Notice: notice, Kind: kind, Name: name})
}

// send publishes the events. The data is already saved, so a failure is only
// logged: the request itself went well.
func (s *Service) send(ctx context.Context, o outbox) {
	for _, in := range o.invites {
		if err := s.pub.Publish(ctx, food.SubjectOrgInvite, in); err != nil {
			s.log.ErrorContext(ctx, "cannot publish an invitation", "err", err, "invite_id", in.InviteID)
		}
	}
	for _, n := range o.notices {
		if err := s.pub.Publish(ctx, food.SubjectOrgNotice, n); err != nil {
			s.log.ErrorContext(ctx, "cannot publish a notice", "err", err, "notice", n.Notice)
		}
	}
}

// cleanName: one space between words, and not empty or too long.
func cleanName(s string, max int) (string, bool) {
	s = strings.Join(strings.Fields(s), " ")
	return s, s != "" && utf8.RuneCountInString(s) <= max
}

// has returns whether a list of members has this person.
func has(members []Member, userID string) bool {
	for _, m := range members {
		if m.UserID == userID {
			return true
		}
	}
	return false
}
