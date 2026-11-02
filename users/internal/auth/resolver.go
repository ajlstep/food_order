package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Resolver answers "who is this?" for a person who wrote on a channel, and
// keeps the state of their channel account.
type Resolver struct {
	store Store
	log   *slog.Logger
	now   func() time.Time
}

// NewResolver makes the resolver. now is the clock (time.Now in production).
func NewResolver(store Store, log *slog.Logger, now func() time.Time) *Resolver {
	return &Resolver{store: store, log: log, now: now}
}

// Who is what the channel tells us about the person who wrote.
type Who struct {
	Channel     string
	ExternalID  string
	ChatRef     string
	Nickname    string
	DisplayName string
	Language    string
}

// Resolved is the answer: the account this person has.
type Resolved struct {
	UserID      string
	Status      string
	IsNew       bool
	Language    string
	DisplayName string
}

var channelName = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// supportedLanguages are the languages of the bot's messages.
var supportedLanguages = map[string]bool{"ro": true, "ru": true, "en": true}

const defaultLanguage = "ro"

// Resolve finds the account of the person, or creates a pending one if the
// person has not written before. It also refreshes what the channel says
// about them (nickname, chat, language).
func (r *Resolver) Resolve(ctx context.Context, w Who) (Resolved, error) {
	w.Channel = strings.ToLower(strings.TrimSpace(w.Channel))
	w.ExternalID = strings.TrimSpace(w.ExternalID)
	if !channelName.MatchString(w.Channel) {
		return Resolved{}, fail("invalid_input", "field", "channel")
	}
	if w.ExternalID == "" || len(w.ExternalID) > 128 {
		return Resolved{}, fail("invalid_input", "field", "external_id")
	}

	var out Resolved
	err := r.store.InTx(ctx, func(tx Tx) error {
		ident, err := tx.IdentityByKey(ctx, w.Channel, w.ExternalID)
		switch {
		case err == nil:
			out, err = r.refresh(ctx, tx, ident, w)
			return err
		case errors.Is(err, ErrNotFound):
			out, err = r.register(ctx, tx, w)
			return err
		default:
			return err
		}
	})
	if err != nil {
		return Resolved{}, fmt.Errorf("resolve identity: %w", err)
	}
	return out, nil
}

// register creates the account of a person we have not seen.
func (r *Resolver) register(ctx context.Context, tx Tx, w Who) (Resolved, error) {
	now := r.now()
	id, err := uuid.NewV7() // sortable by time, needs no coordination between services
	if err != nil {
		return Resolved{}, err
	}
	name := firstNonEmpty(w.DisplayName, w.Nickname)
	lang := pickLanguage(w.Language, "")

	user := User{ID: id.String(), DisplayName: name, Language: lang, Status: StatusPending, CreatedAt: now}
	if err := tx.CreateUser(ctx, user); err != nil {
		return Resolved{}, err
	}
	err = tx.CreateIdentity(ctx, Identity{
		UserID: user.ID, Channel: w.Channel, ExternalID: w.ExternalID, ChatRef: w.ChatRef,
		Nickname: w.Nickname, DisplayName: w.DisplayName, Language: w.Language,
		Status: IdentityActive, LastSeenAt: now, CreatedAt: now,
	})
	if err != nil {
		return Resolved{}, err
	}
	r.log.InfoContext(ctx, "account created", "user_id", user.ID, "channel", w.Channel)
	return Resolved{UserID: user.ID, Status: user.Status, IsNew: true, Language: lang, DisplayName: name}, nil
}

// refresh updates the channel account with what the message carries. A person
// who blocked the bot and writes again is active again; one who sent /stop
// stays stopped (they can still talk to us, they just do not want notices).
func (r *Resolver) refresh(ctx context.Context, tx Tx, ident Identity, w Who) (Resolved, error) {
	user, err := tx.UserByID(ctx, ident.UserID)
	if err != nil {
		return Resolved{}, fmt.Errorf("account %s of identity %d: %w", ident.UserID, ident.ID, err)
	}

	ident.ChatRef = firstNonEmpty(w.ChatRef, ident.ChatRef)
	ident.Nickname = firstNonEmpty(w.Nickname, ident.Nickname)
	ident.DisplayName = firstNonEmpty(w.DisplayName, ident.DisplayName)
	ident.Language = firstNonEmpty(w.Language, ident.Language)
	if ident.Status == IdentityBlocked {
		ident.Status = IdentityActive
	}
	ident.LastSeenAt = r.now()
	if err := tx.UpdateIdentity(ctx, ident); err != nil {
		return Resolved{}, err
	}
	return Resolved{UserID: user.ID, Status: user.Status, Language: user.Language, DisplayName: user.DisplayName}, nil
}

// SetStatus records that the person stopped notifications, blocked the bot,
// or is back.
func (r *Resolver) SetStatus(ctx context.Context, channel, externalID, status string) error {
	switch status {
	case IdentityActive, IdentityStopped, IdentityBlocked:
	default:
		return fail("invalid_input", "field", "status")
	}
	err := r.store.SetIdentityStatus(ctx, strings.ToLower(strings.TrimSpace(channel)), strings.TrimSpace(externalID), status)
	if errors.Is(err, ErrNotFound) {
		return fail("account_not_found")
	}
	if err != nil {
		return fmt.Errorf("set identity status: %w", err)
	}
	return nil
}

// pickLanguage takes the first language we support: the channel's language
// code ("ru", "en-US", "ro") or the fallback.
func pickLanguage(candidates ...string) string {
	for _, c := range candidates {
		c = strings.ToLower(strings.TrimSpace(c))
		if len(c) >= 2 && supportedLanguages[c[:2]] {
			return c[:2]
		}
	}
	return defaultLanguage
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
