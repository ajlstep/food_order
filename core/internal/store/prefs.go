package store

import (
	"context"
)

// Pref is one food preference of a user.
type Pref struct {
	ID    int64
	Kind  string // allergen | dislike | like | diet | target
	Value string
	Hard  bool
}

// Prefs returns the preferences of a user, in the order they were saved.
func (s *Store) Prefs(ctx context.Context, userID int64) ([]Pref, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, value, COALESCE(hard, 0) FROM prefs WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pref{}
	for rows.Next() {
		var p Pref
		var hard int
		if err := rows.Scan(&p.ID, &p.Kind, &p.Value, &hard); err != nil {
			return nil, err
		}
		p.Hard = hard != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReplacePrefs replaces the whole list of a user in one transaction.
func (s *Store) ReplacePrefs(ctx context.Context, userID int64, items []Pref) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM prefs WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, p := range items {
		if _, err := tx.ExecContext(ctx, `INSERT INTO prefs (user_id, kind, value, hard) VALUES (?, ?, ?, ?)`,
			userID, p.Kind, p.Value, boolInt(p.Hard)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
