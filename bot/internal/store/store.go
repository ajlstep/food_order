// Package store is the bot's own SQLite database (bot.db): the users who
// connected to the bot, their contact data and their settings. Nobody else
// opens this file; core only knows users.id (the "user id" of the project).
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

	"food-order/bot/internal/settings"
)

// ErrNotFound is returned when a user does not exist.
var ErrNotFound = errors.New("user not found")

// User statuses.
const (
	StatusActive  = "active"  // gets automatic recommendations
	StatusStopped = "stopped" // sent /stop or blocked the bot; data is kept
)

// User is one person connected to the bot.
type User struct {
	ID           int64 // the id used by core and the other services
	ChatID       int64 // Telegram chat (private chat: same as the Telegram user id)
	TGUserID     int64
	FirstName    string
	LastName     string
	Username     string // Telegram @nickname, without @
	Phone        string // only if the user shared it
	LanguageCode string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Profile is what Telegram tells us about a user at every message.
type Profile struct {
	ChatID       int64
	TGUserID     int64
	FirstName    string
	LastName     string
	Username     string
	LanguageCode string
}

// Store is the bot database.
type Store struct {
	db  *sql.DB
	now func() time.Time
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
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// migrations: append new steps, never change old ones.
var migrations = []string{
	// 1: users, settings, reminders
	`CREATE TABLE users (
		id            INTEGER PRIMARY KEY,
		chat_id       INTEGER NOT NULL UNIQUE,
		tg_user_id    INTEGER,
		first_name    TEXT NOT NULL DEFAULT '',
		last_name     TEXT NOT NULL DEFAULT '',
		username      TEXT NOT NULL DEFAULT '',
		phone         TEXT NOT NULL DEFAULT '',
		language_code TEXT NOT NULL DEFAULT '',
		status        TEXT NOT NULL DEFAULT 'active',
		created_at    TEXT NOT NULL,
		updated_at    TEXT NOT NULL
	);
	CREATE TABLE user_settings (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		key        TEXT NOT NULL,
		value      TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (user_id, key)
	);
	CREATE TABLE reminders (
		user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		for_date   TEXT NOT NULL,
		status     TEXT NOT NULL,              -- pending | sent | failed
		attempts   INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (user_id, for_date)
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
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET version = ?`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ts() string { return s.now().UTC().Format(time.RFC3339) }

const userCols = `id, chat_id, COALESCE(tg_user_id, 0), first_name, last_name, username, phone, language_code, status, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var created, updated string
	err := row.Scan(&u.ID, &u.ChatID, &u.TGUserID, &u.FirstName, &u.LastName, &u.Username, &u.Phone,
		&u.LanguageCode, &u.Status, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	u.CreatedAt, _ = time.Parse(time.RFC3339, created)
	u.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return u, nil
}

// Register creates the user at /start, or refreshes the name/nickname of an
// existing one and makes it active again. created says if it is new.
func (s *Store) Register(ctx context.Context, p Profile) (u User, created bool, err error) {
	now := s.ts()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return u, false, err
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO users (chat_id, tg_user_id, first_name, last_name, username, language_code, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT (chat_id) DO NOTHING`,
		p.ChatID, p.TGUserID, p.FirstName, p.LastName, p.Username, p.LanguageCode, now, now)
	if err != nil {
		return u, false, err
	}
	n, _ := res.RowsAffected()
	created = n == 1
	if !created {
		if _, err := tx.ExecContext(ctx, `
			UPDATE users SET tg_user_id = ?, first_name = ?, last_name = ?, username = ?, language_code = ?,
			                 status = 'active', updated_at = ?
			WHERE chat_id = ?`,
			p.TGUserID, p.FirstName, p.LastName, p.Username, p.LanguageCode, now, p.ChatID); err != nil {
			return u, false, err
		}
	}
	u, err = scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE chat_id = ?`, p.ChatID))
	if err != nil {
		return u, false, err
	}
	return u, created, tx.Commit()
}

// Touch updates name/nickname if they changed since the last message
// (cheap: does nothing when they are the same).
func (s *Store) Touch(ctx context.Context, u User, p Profile) (User, error) {
	if u.FirstName == p.FirstName && u.LastName == p.LastName && u.Username == p.Username && u.LanguageCode == p.LanguageCode {
		return u, nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE users SET first_name = ?, last_name = ?, username = ?, language_code = ?, updated_at = ? WHERE id = ?`,
		p.FirstName, p.LastName, p.Username, p.LanguageCode, s.ts(), u.ID)
	if err != nil {
		return u, err
	}
	u.FirstName, u.LastName, u.Username, u.LanguageCode = p.FirstName, p.LastName, p.Username, p.LanguageCode
	return u, nil
}

// UserByChat finds a user by Telegram chat id.
func (s *Store) UserByChat(ctx context.Context, chatID int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE chat_id = ?`, chatID))
}

// UserByID finds a user by its id.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// SetPhone saves the phone number the user shared.
func (s *Store) SetPhone(ctx context.Context, userID int64, phone string) error {
	return s.exec1(ctx, `UPDATE users SET phone = ?, updated_at = ? WHERE id = ?`, phone, s.ts(), userID)
}

// SetStatus sets active or stopped.
func (s *Store) SetStatus(ctx context.Context, userID int64, status string) error {
	return s.exec1(ctx, `UPDATE users SET status = ?, updated_at = ? WHERE id = ?`, status, s.ts(), userID)
}

func (s *Store) exec1(ctx context.Context, q string, args ...any) error {
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Settings returns the effective settings of a user (saved + defaults).
func (s *Store) Settings(ctx context.Context, userID int64) (settings.Settings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM user_settings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	saved := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		saved[k] = v
	}
	return settings.WithDefaults(saved), rows.Err()
}

// SetSetting saves one value. The caller validates it (settings.Def.Parse).
func (s *Store) SetSetting(ctx context.Context, userID int64, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO user_settings (user_id, key, value, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		userID, key, value, s.ts())
	return err
}

// UserSettings is a user together with its settings.
type UserSettings struct {
	User     User
	Settings settings.Settings
}

// ActiveUsers returns every active user with its settings (for the scheduler).
func (s *Store) ActiveUsers(ctx context.Context) ([]UserSettings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []UserSettings
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, UserSettings{User: u})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Settings, err = s.Settings(ctx, out[i].User.ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// MaxReminderAttempts is how many times the scheduler tries to ask core for
// the automatic recommendation of one day.
const MaxReminderAttempts = 5

// ClaimReminder marks "I am asking the recommendation for this user and day"
// so it is asked only once, also after a restart. It returns false when it
// was already done, or failed too many times. attempt starts at 1.
func (s *Store) ClaimReminder(ctx context.Context, userID int64, forDate string) (attempt int, ok bool, err error) {
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO reminders (user_id, for_date, status, attempts, updated_at) VALUES (?, ?, 'pending', 1, ?)
		ON CONFLICT (user_id, for_date) DO UPDATE
			SET status = 'pending', attempts = reminders.attempts + 1, updated_at = excluded.updated_at
			WHERE reminders.status = 'failed' AND reminders.attempts < ?
		RETURNING attempts`,
		userID, forDate, s.ts(), MaxReminderAttempts).Scan(&attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return attempt, true, nil
}

// FinishReminder records the result of a claimed reminder.
func (s *Store) FinishReminder(ctx context.Context, userID int64, forDate string, ok bool) error {
	status := "sent"
	if !ok {
		status = "failed"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE reminders SET status = ?, updated_at = ? WHERE user_id = ? AND for_date = ?`,
		status, s.ts(), userID, forDate)
	return err
}
