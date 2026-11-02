package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Queryer is what both *sql.DB and *sql.Tx can do, so a store works the same
// inside and outside a transaction.
type Queryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// InTx runs fn in a transaction: if fn returns an error everything it did is
// undone. A refusal for the person must not be returned from fn when the work
// done before it has to stay (see auth.EmailService.Confirm).
func InTx(ctx context.Context, d *sql.DB, fn func(Queryer) error) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Millis turns a time into the integer we store (milliseconds since 1970, UTC).
func Millis(t time.Time) int64 { return t.UnixMilli() }

// Time turns a stored integer back into a time.
func Time(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// NullIfEmpty stores "" as NULL, so that a UNIQUE column can have many "no value".
func NullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// IsUnique says whether err is a violation of a UNIQUE constraint.
func IsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ErrNone is returned by AffectedOne when the statement changed no row.
var ErrNone = errors.New("no row was affected")

// AffectedOne returns ErrNone if the statement changed no row.
func AffectedOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNone
	}
	return nil
}
