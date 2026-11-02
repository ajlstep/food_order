// Package db opens the SQLite file of users and brings its tables up to date.
//
// The tables are described by the files in migrations/, applied in the order
// of their names. A file that was applied is never changed: a change to the
// tables is a new file. Timestamps are integers: milliseconds since 1970, UTC.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/ncruces/go-sqlite3/driver" // database/sql driver "sqlite3", pure Go (no CGO)
	_ "github.com/ncruces/go-sqlite3/embed"  // the SQLite library itself
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Open opens (or creates) the database at path and applies the migrations.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("db: %w", err)
		}
	}
	// WAL: readers do not block the writer. foreign_keys is off by default in SQLite.
	// txlock=immediate: a transaction takes the write lock when it starts, so
	// "read, then write" rules (is the email taken? then take it) cannot interleave.
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=journal_mode(wal)&_pragma=busy_timeout(10000)&_pragma=synchronous(normal)&_pragma=foreign_keys(1)&_txlock=immediate"
	d, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	// One connection: SQLite has one writer anyway, and this way a transaction
	// can never wait for a connection held by the code that started it.
	d.SetMaxOpenConns(1)
	if err := Migrate(ctx, d); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Migrate applies the migrations that were not applied yet, each in its own transaction.
func Migrate(ctx context.Context, d *sql.DB) error {
	if _, err := d.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("db: schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	sort.Strings(names)

	for _, name := range names {
		base := strings.TrimPrefix(name, "migrations/")
		var done int
		if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, base).Scan(&done); err != nil {
			return fmt.Errorf("db: %s: %w", base, err)
		}
		if done > 0 {
			continue
		}
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return fmt.Errorf("db: %s: %w", base, err)
		}
		if err := apply(ctx, d, base, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func apply(ctx context.Context, d *sql.DB, name, body string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: %s: %w", name, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("db: migration %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, strftime('%s','now') * 1000)`, name); err != nil {
		return fmt.Errorf("db: %s: %w", name, err)
	}
	return tx.Commit()
}
