package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"food-order/users/internal/db"
	"food-order/users/internal/mailer"
)

// clock is a clock that moves only when the test says so.
type clock struct{ t time.Time }

func newClock() *clock                   { return &clock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)} }
func (c *clock) Now() time.Time          { return c.t }
func (c *clock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// world is the service with a real (temporary) SQLite database, a fake mailer
// and a fake clock. Using the real store means the SQL is tested together
// with the rules.
type world struct {
	t        *testing.T
	store    *SQLStore
	clock    *clock
	mail     *mailer.Fake
	logs     *bytes.Buffer
	resolver *Resolver
	email    *EmailService
}

func newWorld(t *testing.T) *world {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	w := &world{t: t, store: NewSQLStore(database), clock: newClock(), mail: &mailer.Fake{}, logs: &bytes.Buffer{}}
	log := slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w.resolver = NewResolver(w.store, log, w.clock.Now)
	w.email = NewEmailService(w.store, w.mail, EmailRules{
		CodeTTL: 30 * time.Minute, Resend: 30 * time.Second, MaxAttempts: 5, PerHour: 5,
	}, "test-secret-0123456789", log, w.clock.Now)
	w.email.newCode = func() (string, error) { return "K7P2", nil }
	return w
}

// person writes for the first time from a Telegram account.
func (w *world) person(externalID, nickname string) Resolved {
	w.t.Helper()
	r, err := w.resolver.Resolve(context.Background(), Who{Channel: "telegram", ExternalID: externalID, Nickname: nickname})
	if err != nil {
		w.t.Fatal(err)
	}
	return r
}

// confirmed makes a person with a confirmed email.
func (w *world) confirmed(externalID, email string) string {
	w.t.Helper()
	p := w.person(externalID, "")
	if _, err := w.email.Request(context.Background(), p.UserID, email); err != nil {
		w.t.Fatal(err)
	}
	res, err := w.email.Confirm(context.Background(), p.UserID, "K7P2")
	if err != nil {
		w.t.Fatal(err)
	}
	w.clock.Advance(time.Minute)
	return res.UserID
}

// wantCode checks that err is a refusal with this code and returns it.
func wantCode(t *testing.T, err error, code string) *Error {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected refusal %q, got %v", code, err)
	}
	return e
}
