// Package store is core's own SQLite database (core.db). Nobody else opens
// it. Everything about a person is kept per user_id (users.id from bot.db);
// there is no users table here and no dishes table: orders keep the dish_id
// from catalog plus a snapshot of the dish at the moment of the order.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver" // database/sql driver "sqlite3", pure Go (no CGO)
	_ "github.com/ncruces/go-sqlite3/embed"  // the SQLite library itself
)

// ErrNotFound is returned when a row does not exist (or belongs to another user).
var ErrNotFound = errors.New("not found")

// TimeFormat is how timestamps are stored: always UTC, so strings sort as times.
const TimeFormat = "2006-01-02T15:04:05Z"

// DateFormat is how days are stored (2026-10-28).
const DateFormat = "2006-01-02"

// Store is the core database.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database file and brings the schema up to date.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=synchronous(normal)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite writes one at a time; keeps it simple
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database (for /healthz).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// migrations: append new steps, never change old ones.
var migrations = []string{
	// 1: the schema from the notes (projects/food-order/Core), plus the
	// bookkeeping columns the scheduler needs (marked "+").
	`CREATE TABLE orders (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,          -- users.id from bot.db (no FK: other database)
		date TEXT NOT NULL,                -- 2026-10-28, the day of the meal
		dish_id INTEGER NOT NULL,          -- id from catalog (no FK: other database)
		price REAL,
		rating INTEGER,                    -- NULL until rated
		comment TEXT,
		labels TEXT,                       -- JSON with labels from the comment (llm)
		forced INTEGER DEFAULT 0,          -- 1 = chosen for lack of anything better

		-- snapshot at the moment of the order; every value has min and max
		dish_name TEXT NOT NULL,
		portion_g REAL,
		kcal_min REAL,      kcal_max REAL,
		protein_g_min REAL, protein_g_max REAL,
		fat_g_min REAL,     fat_g_max REAL,
		carbs_g_min REAL,   carbs_g_max REAL,
		nutrition_source TEXT,             -- llm | kitchen | missing

		kitchen_feedback TEXT,             -- + one sentence for the kitchen (llm)
		rating_requested_at TEXT,          -- + when food.rating.requested went out
		rated_at TEXT,                     -- +
		created_at TEXT NOT NULL,          -- +
		updated_at TEXT NOT NULL           -- +
	);
	CREATE INDEX idx_orders_user_date ON orders(user_id, date);

	CREATE TABLE prefs (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		kind TEXT NOT NULL,                -- allergen | dislike | like | diet | target
		value TEXT NOT NULL,
		hard INTEGER DEFAULT 0             -- 1 = hard filter
	);
	CREATE INDEX idx_prefs_user ON prefs(user_id);

	CREATE TABLE recommendations (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL,
		date TEXT NOT NULL,
		request_id TEXT NOT NULL UNIQUE,
		status TEXT NOT NULL,              -- waiting_menu | generating | ready
		picks TEXT,                        -- JSON: [{dish_id, reason}]
		created_at TEXT,
		candidates TEXT,                   -- + JSON: top dishes by score, with what the bot shows
		updated_at TEXT,                   -- + last status change
		notified_at TEXT                   -- + when food.recommendation.ready went out
	);
	CREATE INDEX idx_recommendations_user_date ON recommendations(user_id, date);
	CREATE INDEX idx_recommendations_status ON recommendations(status);

	CREATE TABLE menu_requests (           -- menu requests sent to the source (shared, no user_id)
		request_id TEXT PRIMARY KEY,
		date_from TEXT NOT NULL,
		date_to TEXT NOT NULL,
		status TEXT NOT NULL,              -- requested | ready | error | expired
		error_code TEXT,
		error_message TEXT,
		attempts INTEGER DEFAULT 0,        -- how many times it was sent
		requested_at TEXT,                 -- last time it was sent
		updated_at TEXT,
		next_retry_at TEXT,                -- + NULL = no more retries
		alerted INTEGER DEFAULT 0          -- + food.alert already sent for it
	);
	CREATE INDEX idx_menu_requests_dates ON menu_requests(date_from, date_to);

	CREATE TABLE processed_events (        -- idempotency for NATS
		event_id TEXT PRIMARY KEY,
		type TEXT,
		processed_at TEXT
	);`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	case err != nil:
		return fmt.Errorf("store: migrate: %w", err)
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = ?`, i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
	}
	return nil
}

// ts formats a moment for the database.
func ts(t time.Time) string { return t.UTC().Format(TimeFormat) }

// ParseTS reads a stored timestamp ("" => zero time).
func ParseTS(s string) time.Time {
	t, _ := time.Parse(TimeFormat, s)
	return t
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ----------------------------------------------------------- processed events

// Processed says whether an event id was already handled.
func (s *Store) Processed(ctx context.Context, eventID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM processed_events WHERE event_id = ?`, eventID).Scan(&n)
	return n > 0, err
}

// MarkProcessed remembers an event id.
func (s *Store) MarkProcessed(ctx context.Context, eventID, typ string, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO processed_events (event_id, type, processed_at) VALUES (?, ?, ?)`,
		eventID, typ, ts(now))
	return err
}

// ForgetProcessed drops event ids older than before (JetStream keeps
// messages for a limited time, so older ids cannot come back).
func (s *Store) ForgetProcessed(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM processed_events WHERE processed_at < ?`, ts(before))
	return err
}
