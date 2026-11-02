// Package access decides, before a request goes anywhere, who calls and for
// whom: steps 2-4 of the gateway (step 1 is the request id).
//
//  1. the caller: the bot proves itself with its key (Authorization);
//  2. the person: the bot says who wrote (X-Channel, X-External-ID) and
//     users answers which account that is (rpc.users.auth.resolve);
//  3. the gate: a route for active accounts lets through only accounts
//     with a confirmed email.
//
// The rule "nothing works until the email is confirmed" lives here, in one
// place, and not in the bot. Accounts, codes and sessions stay in users:
// access only applies what users says.
package access

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"food-order/gateway/internal/applog"
	"food-order/gateway/internal/reply"
	"food-order/shared/contracts/gateway"
	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
)

// Level says how much a route needs to know about the person.
type Level int

const (
	// LevelChannel: only who the channel says it is; users is not asked.
	// For identify (it asks users itself) and channel-status.
	LevelChannel Level = iota
	// LevelAccount: an account that is not blocked, also one that still
	// waits for its email. For the routes of the registration.
	LevelAccount
	// LevelActive: an account with a confirmed email. Everything else.
	LevelActive
)

// botChannels are the channels the bot may speak for. A key that leaked from
// the Telegram bot cannot be used to act as somebody's WhatsApp account.
var botChannels = map[string]bool{"telegram": true}

// Resolver finds the account of a person who wrote on a channel.
type Resolver interface {
	Resolve(ctx context.Context, in users.ResolveRequest) (users.ResolveResponse, error)
}

// Access holds what the checks need.
type Access struct {
	botKeyHash [32]byte
	resolver   Resolver
	log        *slog.Logger
}

// New makes the checks. botKey is the key the bot sends.
func New(botKey string, resolver Resolver, log *slog.Logger) *Access {
	return &Access{botKeyHash: sha256.Sum256([]byte(botKey)), resolver: resolver, log: log}
}

// Wrap puts the checks a route of this level needs in front of next.
func (a *Access) Wrap(level Level, next http.Handler) http.Handler {
	h := next
	if level >= LevelAccount {
		h = a.gate(level, h)
		h = a.lookUp(h)
	}
	h = a.identity(h)
	return a.caller(h)
}

// caller checks the key of the calling service.
func (a *Access) caller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get(gateway.HeaderAuthorization), "Bearer ")
		// Comparing hashes takes the same time whatever the key: nobody can
		// find it character by character by measuring the answers.
		given := sha256.Sum256([]byte(key))
		if !ok || subtle.ConstantTimeCompare(given[:], a.botKeyHash[:]) != 1 {
			a.refuse(w, r, rpc.NewError(gateway.ErrUnauthorized, "missing or wrong key"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// identity reads who the person is, as the channel knows them.
func (a *Access) identity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := Identity{
			Channel:    strings.ToLower(strings.TrimSpace(r.Header.Get(gateway.HeaderChannel))),
			ExternalID: strings.TrimSpace(r.Header.Get(gateway.HeaderExternalID)),
		}
		if id.Channel == "" || id.ExternalID == "" {
			a.refuse(w, r, rpc.NewError(gateway.ErrNoIdentity, "X-Channel and X-External-ID are required"))
			return
		}
		if !botChannels[id.Channel] {
			a.refuse(w, r, rpc.NewError(gateway.ErrChannelRefused, "the caller may not speak for "+id.Channel))
			return
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
	})
}

// lookUp asks users which account the person has. users creates a pending
// account for somebody it has not seen yet.
func (a *Access) lookUp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFrom(r.Context())
		res, err := a.resolver.Resolve(r.Context(), users.ResolveRequest{Channel: id.Channel, ExternalID: id.ExternalID})
		if err != nil {
			a.refuse(w, r, err)
			return
		}
		p := Person{UserID: res.UserID, Status: res.Status, Language: res.Language}
		ctx := applog.With(withPerson(r.Context(), p), "user_id", p.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// gate lets through only the accounts the level allows.
func (a *Access) gate(level Level, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := allowed(level, PersonFrom(r.Context()).Status); err != nil {
			a.refuse(w, r, err)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowed is the rule of the gate, apart so it can be read (and tested) alone.
func allowed(level Level, status string) *rpc.Error {
	switch status {
	case users.StatusActive:
		return nil
	case users.StatusPending:
		if level == LevelActive {
			return rpc.NewError(users.ErrEmailNotConfirmed, "confirm the email first")
		}
		return nil
	case users.StatusBlocked:
		return rpc.NewError(users.ErrAccountBlocked, "")
	default: // deleted, or a state the gateway does not know
		return rpc.NewError(rpc.CodeNotFound, "no such account")
	}
}

// refuse answers with err. A "no" is normal and logged only in the detailed
// logs; a failure (users does not answer) is an error.
func (a *Access) refuse(w http.ResponseWriter, r *http.Request, err error) {
	var re *rpc.Error
	if errors.As(err, &re) && reply.Status(re.Code) < 500 {
		a.log.DebugContext(r.Context(), "access refused", "path", r.URL.Path, "code", re.Code)
	} else {
		a.log.ErrorContext(r.Context(), "access failed", "path", r.URL.Path, "err", err)
	}
	reply.Fail(w, r, err)
}
