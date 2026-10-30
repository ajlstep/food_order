// Package store is catalog's own SQLite database (catalog.db). Nobody else
// opens it. It holds everything about food — dishes, the menu of each day,
// weight, components, nutrition per portion (min/max), allergens,
// embeddings — and nothing about users.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver" // database/sql driver "sqlite3", pure Go (no CGO)
	_ "github.com/ncruces/go-sqlite3/embed"  // the SQLite library itself

	"food-order/catalog/internal/contracts"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// TimeFormat is how timestamps are stored: always UTC, so strings sort as times.
const TimeFormat = "2006-01-02T15:04:05Z"

// DateFormat is how days are stored (2026-10-28).
const DateFormat = "2006-01-02"

// Analysis states of a menu item (menu_items.analysis).
const (
	AnalysisNone      = "none"      // nothing is missing
	AnalysisPending   = "pending"   // something is missing; the request to llm is not out yet
	AnalysisRequested = "requested" // food.dish.analyze.requested is out, waiting for llm
	AnalysisDone      = "done"      // llm answered
	AnalysisTimeout   = "timeout"   // llm did not answer in time; the menu went with what there is
	AnalysisOff       = "off"       // something is missing, but analysis is turned off
)

// Store is the catalog database.
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
	// 1: the schema from the notes (projects/food-order/Catalog), plus the
	// bookkeeping columns marked "+".
	`CREATE TABLE dishes (
		id INTEGER PRIMARY KEY,
		external_id TEXT UNIQUE,           -- stable id of the dish, if the source gives one
		name TEXT NOT NULL,
		name_norm TEXT NOT NULL,           -- fallback key when external_id is missing
		embed_requested_at TEXT,           -- + when food.dish.embed.requested went out
		created_at TEXT                    -- +
	);
	CREATE INDEX idx_dishes_name_norm ON dishes(name_norm);

	CREATE TABLE menu_items (
		id INTEGER PRIMARY KEY,
		date TEXT NOT NULL,                -- 2026-10-28
		dish_id INTEGER NOT NULL REFERENCES dishes(id),
		source TEXT NOT NULL,              -- parser | api
		revision INTEGER NOT NULL,
		category TEXT,
		price REAL,
		description TEXT,
		image_url TEXT,

		weight_g REAL,
		weight_text TEXT,                  -- e.g. "250/50"
		weight_source TEXT,                -- site | kitchen | llm | me

		-- nutrition PER PORTION, every value with min and max (exact => min = max)
		kcal_min REAL,            kcal_max REAL,
		protein_g_min REAL,       protein_g_max REAL,
		fat_g_min REAL,           fat_g_max REAL,
		saturated_fat_g_min REAL, saturated_fat_g_max REAL,
		carbs_g_min REAL,         carbs_g_max REAL,
		sugars_g_min REAL,        sugars_g_max REAL,
		fiber_g_min REAL,         fiber_g_max REAL,
		salt_g_min REAL,          salt_g_max REAL,
		nutrition_source TEXT NOT NULL DEFAULT 'missing',   -- llm | kitchen | missing
		nutrition_raw TEXT,                -- JSON as the source sent it

		submitted_at TEXT,
		position INTEGER NOT NULL DEFAULT 0,   -- + order in the source's menu
		source_hash TEXT,                      -- + everything the source gave (changed => new revision)
		input_hash TEXT,                       -- + what llm looks at (changed => llm data is dropped)
		analysis TEXT NOT NULL DEFAULT 'none', -- + none | pending | requested | done | timeout | off
		need TEXT,                             -- + JSON list of what is (was) asked from llm
		analysis_requested_at TEXT,            -- +
		analysis_done_at TEXT,                 -- +
		UNIQUE(date, dish_id)
	);
	CREATE INDEX idx_menu_items_dish ON menu_items(dish_id, date);
	CREATE INDEX idx_menu_items_analysis ON menu_items(analysis);

	CREATE TABLE ingredients (             -- only a dictionary of names; NO nutrition values
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		name_norm TEXT NOT NULL UNIQUE,
		allergens TEXT                     -- JSON, optional
	);

	CREATE TABLE menu_item_components (    -- what the dish of that day is made of
		id INTEGER PRIMARY KEY,
		menu_item_id INTEGER NOT NULL REFERENCES menu_items(id) ON DELETE CASCADE,
		ingredient_id INTEGER REFERENCES ingredients(id),
		name TEXT NOT NULL,
		grams REAL,
		cooking_method TEXT,               -- raw|boiled|steamed|baked|grilled|fried|deep_fried|stewed|smoked|pickled|other
		source TEXT NOT NULL               -- site | llm | kitchen
	);
	CREATE INDEX idx_components_item ON menu_item_components(menu_item_id);

	CREATE TABLE dish_info (
		dish_id INTEGER PRIMARY KEY REFERENCES dishes(id),
		allergens_given TEXT,              -- JSON, from the source (kitchen)
		allergens_possible TEXT,           -- JSON, guessed by the model (not confirmed)
		allergens_confirmed TEXT,          -- JSON, confirmed by a user
		tags TEXT,                         -- JSON
		status TEXT,                       -- pending | analyzed | confirmed
		model TEXT,
		analyzed_at TEXT,
		confirmed_by INTEGER,              -- + users.id of who confirmed (no FK: other database)
		confirmed_at TEXT                  -- +
	);

	CREATE TABLE embeddings (
		dish_id INTEGER PRIMARY KEY REFERENCES dishes(id),
		model TEXT,
		vector BLOB,                       -- float32, little endian
		dims INTEGER,                      -- +
		created_at TEXT                    -- +
	);

	CREATE TABLE menu_requests (           -- + the requests of core (request_id), to answer food.menu.ready
		request_id TEXT PRIMARY KEY,
		source TEXT,
		date_from TEXT NOT NULL,
		date_to TEXT NOT NULL,
		status TEXT NOT NULL,              -- waiting | ready
		dish_ids TEXT,                     -- JSON, filled when ready
		received_at TEXT,
		ready_at TEXT,
		published_at TEXT                  -- food.menu.ready went out
	);
	CREATE INDEX idx_menu_requests_status ON menu_requests(status);

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

// --------------------------------------------------------------- helpers

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

func nullF(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}

func ptrF(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	f := n.Float64
	return &f
}

// jsonList stores a list as JSON; nil stays NULL ("not given").
func jsonList(l []string) any {
	if l == nil {
		return nil
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// readList reads a JSON list column; NULL gives nil.
func readList(n sql.NullString) []string {
	if !n.Valid || n.String == "" {
		return nil
	}
	out := []string{}
	if json.Unmarshal([]byte(n.String), &out) != nil {
		return nil
	}
	return out
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// nutritionCols are the 16 value columns, in the order of Nutrition.Fields.
const nutritionCols = `kcal_min, kcal_max, protein_g_min, protein_g_max, fat_g_min, fat_g_max,
	saturated_fat_g_min, saturated_fat_g_max, carbs_g_min, carbs_g_max,
	sugars_g_min, sugars_g_max, fiber_g_min, fiber_g_max, salt_g_min, salt_g_max`

const nutritionSet = `kcal_min = ?, kcal_max = ?, protein_g_min = ?, protein_g_max = ?, fat_g_min = ?, fat_g_max = ?,
	saturated_fat_g_min = ?, saturated_fat_g_max = ?, carbs_g_min = ?, carbs_g_max = ?,
	sugars_g_min = ?, sugars_g_max = ?, fiber_g_min = ?, fiber_g_max = ?, salt_g_min = ?, salt_g_max = ?`

// nutritionArgs gives the 16 values for nutritionCols (nil => all NULL).
func nutritionArgs(n *contracts.Nutrition) []any {
	out := make([]any, 0, 16)
	var fields []**contracts.Range
	if n != nil {
		fields = n.Fields()
	}
	for i := 0; i < 8; i++ {
		if fields == nil || *fields[i] == nil {
			out = append(out, nil, nil)
			continue
		}
		out = append(out, (*fields[i]).Min, (*fields[i]).Max)
	}
	return out
}

// nutritionScan holds the 16 scanned values.
type nutritionScan [16]sql.NullFloat64

func (ns *nutritionScan) dest() []any {
	out := make([]any, 16)
	for i := range ns {
		out[i] = &ns[i]
	}
	return out
}

// value builds the Nutrition (nil when every value is NULL).
func (ns *nutritionScan) value() *contracts.Nutrition {
	n := &contracts.Nutrition{}
	fields := n.Fields()
	has := false
	for i := 0; i < 8; i++ {
		lo, hi := ns[2*i], ns[2*i+1]
		if !lo.Valid && !hi.Valid {
			continue
		}
		if !lo.Valid {
			lo = hi
		}
		if !hi.Valid {
			hi = lo
		}
		*fields[i] = &contracts.Range{Min: lo.Float64, Max: hi.Float64}
		has = true
	}
	if !has {
		return nil
	}
	return n
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
