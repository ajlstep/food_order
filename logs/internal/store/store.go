// Package store is the logs database (logs.db): one table, written by the
// collector and read by the web UI. Both processes open it; WAL lets the
// reader work while the collector writes.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver" // database/sql driver "sqlite3", pure Go (no CGO)
	_ "github.com/ncruces/go-sqlite3/embed"  // the SQLite library itself
)

// TimeFormat is how timestamps are stored: UTC, with milliseconds, so strings sort as times.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// Entry is one log line.
type Entry struct {
	ID       int64  `json:"id"`
	TS       string `json:"ts"`       // when it happened (from the message, else when NATS got it)
	Received string `json:"received"` // when the collector saved it
	Level    string `json:"level"`    // warn, info, error ... = the part of the subject after "logs."
	Subject  string `json:"subject"`
	Service  string `json:"service"`
	Message  string `json:"message"`
	Fields   string `json:"fields"` // the other JSON fields of the message, or ""
	Seq      uint64 `json:"-"`      // JetStream sequence: a redelivered message is not saved twice
}

// Store is the logs database.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS logs (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	seq      INTEGER UNIQUE,
	ts       TEXT NOT NULL,
	received TEXT NOT NULL,
	level    TEXT NOT NULL,
	subject  TEXT NOT NULL,
	service  TEXT NOT NULL DEFAULT '',
	message  TEXT NOT NULL,
	fields   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS logs_ts ON logs(ts DESC, id DESC);
CREATE INDEX IF NOT EXISTS logs_level ON logs(level, ts DESC);
`

// Open opens (or creates) the database and makes sure the table exists.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)&_pragma=synchronous(normal)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the database.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Add saves one entry. The same JetStream sequence is saved only once.
func (s *Store) Add(ctx context.Context, e Entry) error {
	var seq any
	if e.Seq != 0 {
		seq = e.Seq
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO logs (seq, ts, received, level, subject, service, message, fields)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		seq, e.TS, time.Now().UTC().Format(TimeFormat), e.Level, e.Subject, e.Service, e.Message, e.Fields)
	return err
}

// DeleteBefore removes entries older than t and returns how many.
func (s *Store) DeleteBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM logs WHERE ts < ?`, t.UTC().Format(TimeFormat))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Filter selects entries. Empty fields do not filter.
type Filter struct {
	Levels   []string
	Service  string
	Text     string // substring of message, fields or subject
	From, To string // ts bounds (inclusive / exclusive), TimeFormat
	BeforeID int64  // paging: only entries with a smaller id
	AfterID  int64  // live refresh: only entries with a bigger id
	Limit    int
}

// List returns entries newest first.
func (s *Store) List(ctx context.Context, f Filter) ([]Entry, error) {
	var where []string
	var args []any
	if len(f.Levels) > 0 {
		where = append(where, "level IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.Levels)), ",")+")")
		for _, l := range f.Levels {
			args = append(args, l)
		}
	}
	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Text != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Text) + "%"
		where = append(where, `(message LIKE ? ESCAPE '\' OR fields LIKE ? ESCAPE '\' OR subject LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if f.From != "" {
		where = append(where, "ts >= ?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "ts < ?")
		args = append(args, f.To)
	}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}
	if f.AfterID > 0 {
		where = append(where, "id > ?")
		args = append(args, f.AfterID)
	}
	q := `SELECT id, ts, received, level, subject, service, message, fields FROM logs`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q += " ORDER BY ts DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.TS, &e.Received, &e.Level, &e.Subject, &e.Service, &e.Message, &e.Fields); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Meta lists the values the filters can offer, with counts.
type Meta struct {
	Levels   map[string]int `json:"levels"`
	Services []string       `json:"services"`
	Total    int            `json:"total"`
}

// Meta returns the distinct levels (with counts) and services.
func (s *Store) Meta(ctx context.Context) (Meta, error) {
	m := Meta{Levels: map[string]int{}, Services: []string{}}
	rows, err := s.db.QueryContext(ctx, `SELECT level, COUNT(*) FROM logs GROUP BY level`)
	if err != nil {
		return m, err
	}
	for rows.Next() {
		var l string
		var n int
		if err := rows.Scan(&l, &n); err != nil {
			rows.Close()
			return m, err
		}
		m.Levels[l] = n
		m.Total += n
	}
	rows.Close()
	rows, err = s.db.QueryContext(ctx, `SELECT DISTINCT service FROM logs WHERE service <> '' ORDER BY service`)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var sv string
		if err := rows.Scan(&sv); err != nil {
			return m, err
		}
		m.Services = append(m.Services, sv)
	}
	return m, rows.Err()
}
