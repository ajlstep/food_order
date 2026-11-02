package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"food-order/users/internal/db"
)

// SQLStore keeps the data in SQLite.
type SQLStore struct {
	conn *sql.DB // nil inside a transaction
	q    db.Queryer
}

// NewSQLStore makes the store over an opened database (see package db).
func NewSQLStore(conn *sql.DB) *SQLStore { return &SQLStore{conn: conn, q: conn} }

// InTx implements Store.
func (s *SQLStore) InTx(ctx context.Context, fn func(Tx) error) error {
	return db.InTx(ctx, s.conn, func(q db.Queryer) error { return fn(&SQLStore{q: q}) })
}

// ---------------------------------------------------------------- accounts

const userColumns = `id, display_name, email, language, status, created_at`

func scanUser(row *sql.Row) (User, error) {
	var (
		u       User
		email   sql.NullString
		created int64
	)
	err := row.Scan(&u.ID, &u.DisplayName, &email, &u.Language, &u.Status, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	u.Email, u.CreatedAt = email.String, db.Time(created)
	return u, nil
}

// UserByID implements AccountStore.
func (s *SQLStore) UserByID(ctx context.Context, id string) (User, error) {
	return scanUser(s.q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// UserByEmail implements AccountStore.
func (s *SQLStore) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.q.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE email = ?`, email))
}

// CreateUser implements AccountStore.
func (s *SQLStore) CreateUser(ctx context.Context, u User) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO users (id, display_name, email, language, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.DisplayName, db.NullIfEmpty(u.Email), u.Language, u.Status, db.Millis(u.CreatedAt), db.Millis(u.CreatedAt))
	if db.IsUnique(err) {
		return ErrConflict
	}
	return err
}

// ConfirmEmail implements AccountStore.
func (s *SQLStore) ConfirmEmail(ctx context.Context, userID, email string, when time.Time) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE users SET email = ?, email_confirmed_at = ?, status = 'active', updated_at = ? WHERE id = ?`,
		email, db.Millis(when), db.Millis(when), userID)
	if db.IsUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return mustAffectOne(res)
}

// DeleteUser implements AccountStore.
func (s *SQLStore) DeleteUser(ctx context.Context, id string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
	return err
}

// ---------------------------------------------------------------- channels

const identityColumns = `id, user_id, channel, external_id, chat_ref, nickname, display_name, language, status, last_seen_at, created_at`

// IdentityByKey implements AccountStore.
func (s *SQLStore) IdentityByKey(ctx context.Context, channel, externalID string) (Identity, error) {
	var (
		i             Identity
		seen, created int64
	)
	err := s.q.QueryRowContext(ctx,
		`SELECT `+identityColumns+` FROM identities WHERE channel = ? AND external_id = ?`, channel, externalID).
		Scan(&i.ID, &i.UserID, &i.Channel, &i.ExternalID, &i.ChatRef, &i.Nickname, &i.DisplayName, &i.Language, &i.Status, &seen, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrNotFound
	}
	if err != nil {
		return Identity{}, err
	}
	i.LastSeenAt, i.CreatedAt = db.Time(seen), db.Time(created)
	return i, nil
}

// CreateIdentity implements AccountStore.
func (s *SQLStore) CreateIdentity(ctx context.Context, i Identity) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO identities (user_id, channel, external_id, chat_ref, nickname, display_name, language, status, last_seen_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		i.UserID, i.Channel, i.ExternalID, i.ChatRef, i.Nickname, i.DisplayName, i.Language, i.Status, db.Millis(i.LastSeenAt), db.Millis(i.CreatedAt))
	if db.IsUnique(err) {
		return ErrConflict
	}
	return err
}

// UpdateIdentity implements AccountStore.
func (s *SQLStore) UpdateIdentity(ctx context.Context, i Identity) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE identities SET chat_ref = ?, nickname = ?, display_name = ?, language = ?, status = ?, last_seen_at = ? WHERE id = ?`,
		i.ChatRef, i.Nickname, i.DisplayName, i.Language, i.Status, db.Millis(i.LastSeenAt), i.ID)
	if err != nil {
		return err
	}
	return mustAffectOne(res)
}

// SetIdentityStatus implements AccountStore.
func (s *SQLStore) SetIdentityStatus(ctx context.Context, channel, externalID, status string) error {
	res, err := s.q.ExecContext(ctx, `UPDATE identities SET status = ? WHERE channel = ? AND external_id = ?`, status, channel, externalID)
	if err != nil {
		return err
	}
	return mustAffectOne(res)
}

// MoveIdentities implements AccountStore.
func (s *SQLStore) MoveIdentities(ctx context.Context, fromUser, toUser string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE identities SET user_id = ? WHERE user_id = ?`, toUser, fromUser)
	return err
}

// mustAffectOne turns "no row changed" into ErrNotFound.
func mustAffectOne(res sql.Result) error {
	if err := db.AffectedOne(res); errors.Is(err, db.ErrNone) {
		return ErrNotFound
	} else {
		return err
	}
}

// ---------------------------------------------------------------- codes

// CodeByUser implements CodeStore.
func (s *SQLStore) CodeByUser(ctx context.Context, userID string) (CodeRecord, error) {
	var (
		c                CodeRecord
		created, expires int64
	)
	err := s.q.QueryRowContext(ctx,
		`SELECT user_id, email, code_hash, attempts, created_at, expires_at FROM email_codes WHERE user_id = ?`, userID).
		Scan(&c.UserID, &c.Email, &c.CodeHash, &c.Attempts, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return CodeRecord{}, ErrNotFound
	}
	if err != nil {
		return CodeRecord{}, err
	}
	c.CreatedAt, c.ExpiresAt = db.Time(created), db.Time(expires)
	return c, nil
}

// SaveCode implements CodeStore.
func (s *SQLStore) SaveCode(ctx context.Context, c CodeRecord) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO email_codes (user_id, email, code_hash, attempts, created_at, expires_at) VALUES (?, ?, ?, 0, ?, ?)
		 ON CONFLICT(user_id) DO UPDATE SET email = excluded.email, code_hash = excluded.code_hash,
		   attempts = 0, created_at = excluded.created_at, expires_at = excluded.expires_at`,
		c.UserID, c.Email, c.CodeHash, db.Millis(c.CreatedAt), db.Millis(c.ExpiresAt))
	return err
}

// AddWrongAttempt implements CodeStore.
func (s *SQLStore) AddWrongAttempt(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.q.QueryRowContext(ctx, `UPDATE email_codes SET attempts = attempts + 1 WHERE user_id = ? RETURNING attempts`, userID).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return n, err
}

// DeleteCode implements CodeStore.
func (s *SQLStore) DeleteCode(ctx context.Context, userID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM email_codes WHERE user_id = ?`, userID)
	return err
}

// LastSend implements CodeStore.
func (s *SQLStore) LastSend(ctx context.Context, userID string) (time.Time, error) {
	var v sql.NullInt64
	if err := s.q.QueryRowContext(ctx, `SELECT MAX(sent_at) FROM email_code_sends WHERE user_id = ?`, userID).Scan(&v); err != nil {
		return time.Time{}, err
	}
	if !v.Valid {
		return time.Time{}, ErrNotFound
	}
	return db.Time(v.Int64), nil
}

// RecordSend implements CodeStore.
func (s *SQLStore) RecordSend(ctx context.Context, userID, email string, when time.Time) error {
	_, err := s.q.ExecContext(ctx, `INSERT INTO email_code_sends (user_id, email, sent_at) VALUES (?, ?, ?)`, userID, email, db.Millis(when))
	return err
}

// SendsSince implements CodeStore.
func (s *SQLStore) SendsSince(ctx context.Context, email string, since time.Time) (int, time.Time, error) {
	var (
		n      int
		oldest sql.NullInt64
	)
	err := s.q.QueryRowContext(ctx, `SELECT COUNT(*), MIN(sent_at) FROM email_code_sends WHERE email = ? AND sent_at > ?`, email, db.Millis(since)).Scan(&n, &oldest)
	if err != nil {
		return 0, time.Time{}, err
	}
	if !oldest.Valid {
		return n, time.Time{}, nil
	}
	return n, db.Time(oldest.Int64), nil
}

// ForgetSendsBefore implements CodeStore.
func (s *SQLStore) ForgetSendsBefore(ctx context.Context, before time.Time) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM email_code_sends WHERE sent_at < ?`, db.Millis(before))
	return err
}
