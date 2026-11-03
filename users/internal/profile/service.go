package profile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"food-order/users/internal/refusal"
)

// Service holds the rules of the profile.
type Service struct {
	store Store
	log   *slog.Logger
	now   func() time.Time
}

// NewService makes the service. now is the clock (time.Now in production).
func NewService(store Store, log *slog.Logger, now func() time.Time) *Service {
	return &Service{store: store, log: log, now: now}
}

var fail = refusal.New

// Limits, so that nobody fills the database through the bot.
const (
	maxNameLen   = 64
	maxLabelLen  = 40
	maxAllergens = 30
	maxPrefs     = 200
	maxValueLen  = 100
	maxLocations = 20
	maxAddrLen   = 200
	maxNotesLen  = 200
)

var languages = []string{"ro", "ru", "en"}

// active returns the account if it may use the profile: the email must be
// confirmed ("nothing works before it") and the account not blocked.
func (s *Service) active(ctx context.Context, q AccountStore, userID string) (Account, error) {
	a, err := q.Account(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return Account{}, fail("account_not_found")
	}
	if err != nil {
		return Account{}, fmt.Errorf("load account: %w", err)
	}
	switch a.Status {
	case StatusActive:
		return a, nil
	case StatusPending:
		return Account{}, fail("email_not_confirmed")
	default:
		return Account{}, fail("account_blocked")
	}
}

// Profile returns the person's own data.
func (s *Service) Profile(ctx context.Context, userID string) (Account, error) {
	return s.active(ctx, s.store, userID)
}

// ProfileChange says what to change; nil means "leave as it is".
type ProfileChange struct {
	DisplayName *string
	Language    *string
}

// UpdateProfile changes the name and/or the language.
func (s *Service) UpdateProfile(ctx context.Context, userID string, c ProfileChange) (Account, error) {
	a, err := s.active(ctx, s.store, userID)
	if err != nil {
		return Account{}, err
	}
	if c.DisplayName != nil {
		name := strings.Join(strings.Fields(*c.DisplayName), " ")
		if name == "" || tooLong(name, maxNameLen) {
			return Account{}, fail("invalid_input", "field", "display_name")
		}
		a.DisplayName = name
	}
	if c.Language != nil {
		lang := strings.ToLower(strings.TrimSpace(*c.Language))
		if !slices.Contains(languages, lang) {
			return Account{}, fail("invalid_input", "field", "language")
		}
		a.Language = lang
	}
	if err := s.store.SaveAccount(ctx, a, s.now()); err != nil {
		return Account{}, fmt.Errorf("save account: %w", err)
	}
	return a, nil
}

// SetPhone saves the phone. Unlike the rest of the profile it works for a
// pending account too: the bot asks for the phone while the person registers.
// The caller has checked that the phone is the person's own.
func (s *Service) SetPhone(ctx context.Context, userID, rawPhone string) error {
	phone, ok := NormPhone(rawPhone)
	if !ok {
		return fail("invalid_phone")
	}
	err := s.store.SetPhone(ctx, userID, phone, s.now())
	switch {
	case errors.Is(err, ErrNotFound):
		return fail("account_not_found")
	case errors.Is(err, ErrConflict):
		return fail("phone_taken")
	case err != nil:
		return fmt.Errorf("save phone: %w", err)
	}
	return nil
}

// Channels lists the person's channels, and the channel to use for a message:
// the active one seen most recently.
func (s *Service) Channels(ctx context.Context, userID string) (list []Channel, latest string, err error) {
	if _, err := s.active(ctx, s.store, userID); err != nil {
		return nil, "", err
	}
	list, err = s.store.Channels(ctx, userID)
	if err != nil {
		return nil, "", fmt.Errorf("load channels: %w", err)
	}
	for _, c := range list { // the store gives the most recent first
		if c.Status == "active" {
			return list, c.Channel, nil
		}
	}
	return list, "", nil
}

// Settings returns every setting: the saved value, or the default.
func (s *Service) Settings(ctx context.Context, userID string) (map[string]string, error) {
	if _, err := s.active(ctx, s.store, userID); err != nil {
		return nil, err
	}
	return s.readSettings(ctx, s.store, userID)
}

func (s *Service) readSettings(ctx context.Context, q SettingsStore, userID string) (map[string]string, error) {
	saved, err := q.Settings(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	out := defaultSettings()
	for k := range out {
		if v, ok := saved[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// SetSettings changes the given settings. If one value is not allowed
// nothing is changed, and the refusal names the key.
func (s *Service) SetSettings(ctx context.Context, userID string, values map[string]string) (map[string]string, error) {
	clean := make(map[string]string, len(values))
	for key, raw := range values {
		canon, known, err := canonSetting(key, raw)
		if !known {
			return nil, fail("invalid_input", "field", key, "reason", "unknown setting")
		}
		if err != nil {
			return nil, fail("invalid_input", "field", key, "reason", err.Error())
		}
		clean[key] = canon
	}

	var out map[string]string
	err := s.store.InTx(ctx, func(tx Tx) error {
		if _, err := s.active(ctx, tx, userID); err != nil {
			return err // a refusal: nothing was written yet, so rolling back loses nothing
		}
		if err := tx.SaveSettings(ctx, userID, clean); err != nil {
			return err
		}
		var err error
		out, err = s.readSettings(ctx, tx, userID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("set settings: %w", err)
	}
	return out, nil
}
