package profile

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

// DueCandidates implements Store. One row per saved setting; a person with
// none has one row with NULLs.
func (s *SQLStore) DueCandidates(ctx context.Context) ([]DueCandidate, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT u.id, us.key, us.value
		FROM users u LEFT JOIN user_settings us ON us.user_id = u.id
		WHERE u.status = 'active'
		  AND EXISTS (SELECT 1 FROM identities i WHERE i.user_id = u.id AND i.status = 'active')
		ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueCandidate
	for rows.Next() {
		var (
			id         string
			key, value sql.NullString
		)
		if err := rows.Scan(&id, &key, &value); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].UserID != id {
			out = append(out, DueCandidate{UserID: id, Saved: map[string]string{}})
		}
		if key.Valid {
			out[len(out)-1].Saved[key.String] = value.String
		}
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- account

// Account implements AccountStore.
func (s *SQLStore) Account(ctx context.Context, userID string) (Account, error) {
	var (
		a            Account
		email, phone sql.NullString
	)
	err := s.q.QueryRowContext(ctx,
		`SELECT id, display_name, email, phone, language, status FROM users WHERE id = ?`, userID).
		Scan(&a.ID, &a.DisplayName, &email, &phone, &a.Language, &a.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	a.Email, a.Phone = email.String, phone.String
	return a, nil
}

// SaveAccount implements AccountStore.
func (s *SQLStore) SaveAccount(ctx context.Context, a Account, at time.Time) error {
	res, err := s.q.ExecContext(ctx,
		`UPDATE users SET display_name = ?, language = ?, updated_at = ? WHERE id = ?`,
		a.DisplayName, a.Language, db.Millis(at), a.ID)
	if err != nil {
		return err
	}
	return notFound(res)
}

// SetPhone implements AccountStore.
func (s *SQLStore) SetPhone(ctx context.Context, userID, phone string, at time.Time) error {
	res, err := s.q.ExecContext(ctx, `UPDATE users SET phone = ?, updated_at = ? WHERE id = ?`, phone, db.Millis(at), userID)
	if db.IsUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return notFound(res)
}

// Channels implements AccountStore.
func (s *SQLStore) Channels(ctx context.Context, userID string) ([]Channel, error) {
	rows, err := s.q.QueryContext(ctx,
		`SELECT channel, chat_ref, status, last_seen_at FROM identities WHERE user_id = ? ORDER BY last_seen_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Channel
	for rows.Next() {
		var (
			c    Channel
			seen int64
		)
		if err := rows.Scan(&c.Channel, &c.ChatRef, &c.Status, &seen); err != nil {
			return nil, err
		}
		c.LastSeenAt = db.Time(seen)
		out = append(out, c)
	}
	return out, rows.Err()
}

// notFound turns "no row changed" into ErrNotFound.
func notFound(res sql.Result) error {
	if err := db.AffectedOne(res); errors.Is(err, db.ErrNone) {
		return ErrNotFound
	} else {
		return err
	}
}

// ---------------------------------------------------------------- settings

// Settings implements SettingsStore.
func (s *SQLStore) Settings(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT key, value FROM user_settings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SaveSettings implements SettingsStore.
func (s *SQLStore) SaveSettings(ctx context.Context, userID string, values map[string]string) error {
	for k, v := range values {
		_, err := s.q.ExecContext(ctx,
			`INSERT INTO user_settings (user_id, key, value) VALUES (?, ?, ?)
			 ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, userID, k, v)
		if err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- food

// Allergens implements FoodStore.
func (s *SQLStore) Allergens(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT label FROM allergens WHERE user_id = ? ORDER BY rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ReplaceAllergens implements FoodStore.
func (s *SQLStore) ReplaceAllergens(ctx context.Context, userID string, labels []string) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM allergens WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, l := range labels {
		if _, err := s.q.ExecContext(ctx, `INSERT INTO allergens (user_id, label) VALUES (?, ?)`, userID, l); err != nil {
			return err
		}
	}
	return nil
}

// Prefs implements FoodStore.
func (s *SQLStore) Prefs(ctx context.Context, userID string) ([]Pref, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT kind, value, hard FROM preferences WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pref{}
	for rows.Next() {
		var (
			p    Pref
			hard int
		)
		if err := rows.Scan(&p.Kind, &p.Value, &hard); err != nil {
			return nil, err
		}
		p.Hard = hard != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReplacePrefs implements FoodStore.
func (s *SQLStore) ReplacePrefs(ctx context.Context, userID string, items []Pref) error {
	if _, err := s.q.ExecContext(ctx, `DELETE FROM preferences WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, p := range items {
		hard := 0
		if p.Hard {
			hard = 1
		}
		if _, err := s.q.ExecContext(ctx,
			`INSERT INTO preferences (user_id, kind, value, hard) VALUES (?, ?, ?, ?)`, userID, p.Kind, p.Value, hard); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- locations

const locationColumns = `id, label, address, lat, lon, notes, is_default`

func scanLocation(sc interface{ Scan(...any) error }) (Location, error) {
	var (
		l        Location
		lat, lon sql.NullFloat64
		def      int
	)
	if err := sc.Scan(&l.ID, &l.Label, &l.Address, &lat, &lon, &l.Notes, &def); err != nil {
		return Location{}, err
	}
	if lat.Valid && lon.Valid {
		l.Lat, l.Lon = &lat.Float64, &lon.Float64
	}
	l.IsDefault = def != 0
	return l, nil
}

// Locations implements LocationStore.
func (s *SQLStore) Locations(ctx context.Context, userID string) ([]Location, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+locationColumns+` FROM locations WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SaveLocation implements LocationStore. is_default is not touched here:
// the default moves only through SetDefaultLocation.
func (s *SQLStore) SaveLocation(ctx context.Context, userID string, l Location, at time.Time) (Location, error) {
	if l.ID == 0 {
		res, err := s.q.ExecContext(ctx,
			`INSERT INTO locations (user_id, label, address, lat, lon, notes, is_default, created_at) VALUES (?, ?, ?, ?, ?, ?, 0, ?)`,
			userID, l.Label, l.Address, l.Lat, l.Lon, l.Notes, db.Millis(at))
		if err != nil {
			return Location{}, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return Location{}, err
		}
		l.ID = id
	} else {
		res, err := s.q.ExecContext(ctx,
			`UPDATE locations SET label = ?, address = ?, lat = ?, lon = ?, notes = ? WHERE id = ? AND user_id = ?`,
			l.Label, l.Address, l.Lat, l.Lon, l.Notes, l.ID, userID)
		if err != nil {
			return Location{}, err
		}
		if err := notFound(res); err != nil {
			return Location{}, err
		}
	}
	saved, err := scanLocation(s.q.QueryRowContext(ctx, `SELECT `+locationColumns+` FROM locations WHERE id = ?`, l.ID))
	return saved, err
}

// DeleteLocation implements LocationStore.
func (s *SQLStore) DeleteLocation(ctx context.Context, userID string, id int64) (bool, error) {
	var def int
	err := s.q.QueryRowContext(ctx, `SELECT is_default FROM locations WHERE id = ? AND user_id = ?`, id, userID).Scan(&def)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if _, err := s.q.ExecContext(ctx, `DELETE FROM locations WHERE id = ? AND user_id = ?`, id, userID); err != nil {
		return false, err
	}
	return def != 0, nil
}

// SetDefaultLocation implements LocationStore.
func (s *SQLStore) SetDefaultLocation(ctx context.Context, userID string, id int64) error {
	var one int
	err := s.q.QueryRowContext(ctx, `SELECT 1 FROM locations WHERE id = ? AND user_id = ?`, id, userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// first clear, then set: the unique index allows only one default at any moment
	if _, err := s.q.ExecContext(ctx, `UPDATE locations SET is_default = 0 WHERE user_id = ? AND is_default = 1`, userID); err != nil {
		return err
	}
	_, err = s.q.ExecContext(ctx, `UPDATE locations SET is_default = 1 WHERE id = ?`, id)
	return err
}
