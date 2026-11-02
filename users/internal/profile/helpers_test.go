package profile

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"food-order/users/internal/db"
	"food-order/users/internal/refusal"
)

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

// world is the service over a real temporary SQLite database.
type world struct {
	t     *testing.T
	store *SQLStore
	svc   *Service
	clock *clock
	logs  *bytes.Buffer
}

func newWorld(t *testing.T) *world {
	t.Helper()
	database, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	w := &world{t: t, store: NewSQLStore(database), clock: &clock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)}, logs: &bytes.Buffer{}}
	w.svc = NewService(w.store, slog.New(slog.NewTextHandler(w.logs, nil)), w.clock.Now)
	return w
}

// user inserts an account directly, so that these tests do not depend on package auth.
func (w *world) user(id, status string) string {
	w.t.Helper()
	_, err := w.store.q.ExecContext(context.Background(),
		`INSERT INTO users (id, display_name, status, created_at, updated_at) VALUES (?, ?, ?, 1, 1)`, id, "name-"+id, status)
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

func (w *world) active(id string) string { return w.user(id, StatusActive) }

func (w *world) channel(userID, channel, externalID, status string, seen int64) {
	w.t.Helper()
	_, err := w.store.q.ExecContext(context.Background(),
		`INSERT INTO identities (user_id, channel, external_id, status, last_seen_at, created_at) VALUES (?, ?, ?, ?, ?, 1)`,
		userID, channel, externalID, status, seen)
	if err != nil {
		w.t.Fatal(err)
	}
}

// wantCode checks that err is a refusal with this code and returns it.
func wantCode(t *testing.T, err error, code string) *refusal.Error {
	t.Helper()
	var e *refusal.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected refusal %q, got %v", code, err)
	}
	return e
}
