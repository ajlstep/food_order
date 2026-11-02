package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"food-order/users/internal/mailer"
)

// EmailRules are the limits of the confirmation. Their values come from the settings.
type EmailRules struct {
	CodeTTL     time.Duration // how long a code works
	Resend      time.Duration // pause before a new code may be asked
	MaxAttempts int           // wrong attempts before the code is cancelled
	PerHour     int           // codes sent to one address in an hour
}

// EmailService confirms the email of a pending account.
//
// The rules (see services/Users.md in the vault): nothing works before the
// email is confirmed; a code has 4 characters and works 30 minutes; a new
// code is allowed after 30 seconds; 5 wrong attempts cancel the code; at most
// 5 codes an hour for one address. The service never says whether an address
// already belongs to an account: that shows only after the right code.
type EmailService struct {
	store  Store
	mail   mailer.Mailer
	rules  EmailRules
	secret []byte
	log    *slog.Logger
	now    func() time.Time

	newCode func() (string, error) // replaced in tests
	sending sync.WaitGroup
}

// NewEmailService makes the service. secret signs the codes, now is the clock.
func NewEmailService(store Store, m mailer.Mailer, rules EmailRules, secret string, log *slog.Logger, now func() time.Time) *EmailService {
	return &EmailService{store: store, mail: m, rules: rules, secret: []byte(secret), log: log, now: now, newCode: randomCode}
}

// Wait blocks until the letters being sent have left. It is for a clean stop
// of the service, and for tests.
func (s *EmailService) Wait() { s.sending.Wait() }

// CodeSent is the result of a request for a code.
type CodeSent struct{ ValidFor time.Duration }

// Request makes a code and sends it to the address in the background.
func (s *EmailService) Request(ctx context.Context, userID, rawEmail string) (CodeSent, error) {
	email, ok := normalizeEmail(rawEmail)
	if !ok {
		return CodeSent{}, fail("invalid_email")
	}
	code, err := s.newCode()
	if err != nil {
		return CodeSent{}, err
	}

	var refusal *Error
	err = s.store.InTx(ctx, func(tx Tx) error {
		user, err := tx.UserByID(ctx, userID)
		if errors.Is(err, ErrNotFound) {
			refusal = fail("account_not_found")
			return nil
		}
		if err != nil {
			return err
		}
		if refusal = checkCanConfirm(user); refusal != nil {
			return nil
		}

		now := s.now()
		if refusal, err = s.checkLimits(ctx, tx, userID, email, now); refusal != nil || err != nil {
			return err
		}
		if err := tx.SaveCode(ctx, CodeRecord{
			UserID: userID, Email: email, CodeHash: hashCode(s.secret, userID, email, code),
			CreatedAt: now, ExpiresAt: now.Add(s.rules.CodeTTL),
		}); err != nil {
			return err
		}
		if err := tx.RecordSend(ctx, userID, email, now); err != nil {
			return err
		}
		return tx.ForgetSendsBefore(ctx, now.Add(-24*time.Hour))
	})
	if err != nil {
		return CodeSent{}, fmt.Errorf("request email code: %w", err)
	}
	if refusal != nil {
		return CodeSent{}, refusal
	}

	s.sendInBackground(userID, email, code)
	return CodeSent{ValidFor: s.rules.CodeTTL}, nil
}

// checkLimits applies the pause between codes and the limit per hour.
func (s *EmailService) checkLimits(ctx context.Context, tx Tx, userID, email string, now time.Time) (*Error, error) {
	last, err := tx.LastSend(ctx, userID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if err == nil {
		if wait := s.rules.Resend - now.Sub(last); wait > 0 {
			return fail("cooldown", "retry_after", seconds(wait)), nil
		}
	}

	hourAgo := now.Add(-time.Hour)
	n, oldest, err := tx.SendsSince(ctx, email, hourAgo)
	if err != nil {
		return nil, err
	}
	if n >= s.rules.PerHour {
		return fail("too_many_codes", "retry_after", seconds(oldest.Add(time.Hour).Sub(now))), nil
	}
	return nil, nil
}

// checkCanConfirm says whether this account may ask for or use a code.
func checkCanConfirm(u User) *Error {
	switch u.Status {
	case StatusPending:
		return nil
	case StatusActive:
		return fail("already_confirmed")
	default:
		return fail("account_blocked")
	}
}

// seconds rounds up, so that "try again in 0 seconds" never happens.
func seconds(d time.Duration) int { return int(math.Ceil(d.Seconds())) }

// sendInBackground sends the letter without making the person wait for the
// mail server. If it fails the person can ask for a new code after the pause.
func (s *EmailService) sendInBackground(userID, email, code string) {
	msg := confirmationLetter(email, code, s.rules.CodeTTL)
	s.sending.Add(1)
	go func() {
		defer s.sending.Done()
		// Not the request's context: the request is over by the time this runs.
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.mail.Send(ctx, msg); err != nil {
			// Mail servers repeat the address in their errors; it must not get into the logs.
			s.log.Error("could not send the confirmation email", "user_id", userID,
				"err", strings.ReplaceAll(err.Error(), email, "<email>"))
			return
		}
		s.log.Info("confirmation email sent", "user_id", userID)
	}()
}

// confirmationLetter is the text of the letter. Romanian for now; the account
// has a language, so other languages are an addition here.
func confirmationLetter(to, code string, ttl time.Duration) mailer.Message {
	return mailer.Message{
		To:      to,
		Subject: "Cod de confirmare food-order",
		Body: fmt.Sprintf("Codul tău de confirmare: %s\n\nÎl scrii în chat. Este valabil %d de minute.\n"+
			"Dacă n-ai cerut tu acest cod, ignoră mesajul.\n", code, int(ttl.Minutes())),
	}
}

// Confirmed is the result of a right code.
type Confirmed struct {
	UserID   string // the account to use from now on; after Attached it is another one
	Attached bool   // the channel moved to the account that already owned the email
}

// Confirm checks the code the person typed.
func (s *EmailService) Confirm(ctx context.Context, userID, rawCode string) (Confirmed, error) {
	code, ok := normalizeCode(rawCode)
	if !ok {
		return Confirmed{}, fail("invalid_input", "field", "code")
	}

	var (
		out     Confirmed
		refusal *Error
	)
	// A wrong attempt must be counted even though the person gets a refusal,
	// so a refusal is not returned from the transaction (that would undo it).
	err := s.store.InTx(ctx, func(tx Tx) error {
		user, err := tx.UserByID(ctx, userID)
		if errors.Is(err, ErrNotFound) {
			refusal = fail("account_not_found")
			return nil
		}
		if err != nil {
			return err
		}
		if refusal = checkCanConfirm(user); refusal != nil {
			return nil
		}
		rec, err := tx.CodeByUser(ctx, userID)
		if errors.Is(err, ErrNotFound) {
			refusal = fail("no_code")
			return nil
		}
		if err != nil {
			return err
		}
		if !s.now().Before(rec.ExpiresAt) {
			refusal = fail("expired")
			return tx.DeleteCode(ctx, userID)
		}
		if !sameHash(rec.CodeHash, hashCode(s.secret, userID, rec.Email, code)) {
			refusal, err = s.countWrongAttempt(ctx, tx, userID)
			return err
		}
		if err := tx.DeleteCode(ctx, userID); err != nil { // a code works once
			return err
		}
		out, refusal, err = s.applyEmail(ctx, tx, user, rec.Email)
		return err
	})
	if err != nil {
		return Confirmed{}, fmt.Errorf("confirm email: %w", err)
	}
	if refusal != nil {
		return Confirmed{}, refusal
	}
	return out, nil
}

func (s *EmailService) countWrongAttempt(ctx context.Context, tx Tx, userID string) (*Error, error) {
	n, err := tx.AddWrongAttempt(ctx, userID)
	if err != nil {
		return nil, err
	}
	left := s.rules.MaxAttempts - n
	if left > 0 {
		return fail("wrong_code", "attempts_left", left), nil
	}
	return fail("too_many_attempts"), tx.DeleteCode(ctx, userID)
}

// applyEmail gives the confirmed email to the account. If another account
// already owns that email, the person is the same one on a new channel: the
// channel moves there and the empty pending account disappears ("like in git":
// no merge rules, because nothing could be done before the confirmation, so
// the pending account has no data).
func (s *EmailService) applyEmail(ctx context.Context, tx Tx, pending User, email string) (Confirmed, *Error, error) {
	owner, err := tx.UserByEmail(ctx, email)
	switch {
	case errors.Is(err, ErrNotFound):
		if err := tx.ConfirmEmail(ctx, pending.ID, email, s.now()); err != nil {
			return Confirmed{}, nil, err
		}
		s.log.InfoContext(ctx, "email confirmed", "user_id", pending.ID)
		return Confirmed{UserID: pending.ID}, nil, nil
	case err != nil:
		return Confirmed{}, nil, err
	case owner.Status == StatusBlocked || owner.Status == StatusDeleted:
		return Confirmed{}, fail("account_blocked"), nil
	}

	if err := tx.MoveIdentities(ctx, pending.ID, owner.ID); err != nil {
		return Confirmed{}, nil, err
	}
	if err := tx.DeleteUser(ctx, pending.ID); err != nil {
		return Confirmed{}, nil, err
	}
	s.log.InfoContext(ctx, "channel attached to an existing account", "user_id", owner.ID, "removed_pending_user_id", pending.ID)
	return Confirmed{UserID: owner.ID, Attached: true}, nil, nil
}
