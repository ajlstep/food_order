package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Recommendation statuses.
const (
	RecWaitingMenu = "waiting_menu" // the menu of the day is not in catalog yet
	RecGenerating  = "generating"   // scored, waiting for llm
	RecReady       = "ready"        // picks saved
)

// PickRef is what the picks column keeps: the dish and the reason.
type PickRef struct {
	DishID int64  `json:"dish_id"`
	Reason string `json:"reason"`
}

// Candidate is one of the top dishes by score, with everything the bot
// shows and everything an order needs if catalog is down at that moment.
type Candidate struct {
	DishID            int64    `json:"dish_id"`
	Name              string   `json:"name"`
	Price             *float64 `json:"price,omitempty"`
	Score             float64  `json:"score"`
	AvgRating         *float64 `json:"avg_rating,omitempty"`
	Tags              []string `json:"tags,omitempty"`
	AllergensPossible []string `json:"allergens_possible,omitempty"`
	PortionG          *float64 `json:"portion_g,omitempty"`
	KcalMin           *float64 `json:"kcal_min,omitempty"`
	KcalMax           *float64 `json:"kcal_max,omitempty"`
	ProteinGMin       *float64 `json:"protein_g_min,omitempty"`
	ProteinGMax       *float64 `json:"protein_g_max,omitempty"`
	FatGMin           *float64 `json:"fat_g_min,omitempty"`
	FatGMax           *float64 `json:"fat_g_max,omitempty"`
	CarbsGMin         *float64 `json:"carbs_g_min,omitempty"`
	CarbsGMax         *float64 `json:"carbs_g_max,omitempty"`
	NutritionSource   string   `json:"nutrition_source,omitempty"`
}

// Rec is one recommendation of a user for a day.
type Rec struct {
	ID         int64
	UserID     int64
	Date       string
	RequestID  string
	Status     string
	Picks      []PickRef
	Candidates []Candidate
	CreatedAt  string
	UpdatedAt  string
	NotifiedAt string
}

const recCols = `id, user_id, date, request_id, status, COALESCE(picks, ''), COALESCE(candidates, ''),
	COALESCE(created_at, ''), COALESCE(updated_at, ''), COALESCE(notified_at, '')`

func scanRec(r scanner) (Rec, error) {
	var rec Rec
	var picks, cands string
	if err := r.Scan(&rec.ID, &rec.UserID, &rec.Date, &rec.RequestID, &rec.Status, &picks, &cands,
		&rec.CreatedAt, &rec.UpdatedAt, &rec.NotifiedAt); err != nil {
		return Rec{}, err
	}
	if picks != "" {
		if err := json.Unmarshal([]byte(picks), &rec.Picks); err != nil {
			return Rec{}, fmt.Errorf("recommendation %d: picks: %w", rec.ID, err)
		}
	}
	if cands != "" {
		if err := json.Unmarshal([]byte(cands), &rec.Candidates); err != nil {
			return Rec{}, fmt.Errorf("recommendation %d: candidates: %w", rec.ID, err)
		}
	}
	return rec, nil
}

// CreateRec saves a new recommendation in status waiting_menu.
func (s *Store) CreateRec(ctx context.Context, userID int64, date, requestID string, now time.Time) (Rec, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO recommendations (user_id, date, request_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, userID, date, requestID, RecWaitingMenu, ts(now), ts(now))
	if err != nil {
		return Rec{}, err
	}
	id, _ := res.LastInsertId()
	return s.RecByID(ctx, id)
}

// RecByID returns one recommendation.
func (s *Store) RecByID(ctx context.Context, id int64) (Rec, error) {
	return s.oneRec(ctx, `SELECT `+recCols+` FROM recommendations WHERE id = ?`, id)
}

// RecByRequestID returns the recommendation of a request sent to llm.
func (s *Store) RecByRequestID(ctx context.Context, requestID string) (Rec, error) {
	return s.oneRec(ctx, `SELECT `+recCols+` FROM recommendations WHERE request_id = ?`, requestID)
}

// LatestRec returns the last recommendation of a user for a day.
func (s *Store) LatestRec(ctx context.Context, userID int64, date string) (Rec, error) {
	return s.oneRec(ctx, `SELECT `+recCols+` FROM recommendations WHERE user_id = ? AND date = ?
		ORDER BY id DESC LIMIT 1`, userID, date)
}

func (s *Store) oneRec(ctx context.Context, q string, args ...any) (Rec, error) {
	r, err := scanRec(s.db.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Rec{}, ErrNotFound
	}
	return r, err
}

// ClaimRec moves a recommendation from one status to another only if it is
// still in the first one. It returns false if someone else moved it first,
// so two goroutines never generate (or finish) the same recommendation.
func (s *Store) ClaimRec(ctx context.Context, id int64, from, to string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE recommendations SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
		to, ts(now), id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// SetCandidates saves the top dishes by score.
func (s *Store) SetCandidates(ctx context.Context, id int64, cands []Candidate) error {
	if cands == nil {
		cands = []Candidate{}
	}
	b, err := json.Marshal(cands)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE recommendations SET candidates = ? WHERE id = ?`, string(b), id)
	return err
}

// FinishRec saves the picks and moves generating -> ready. false = it was
// not in generating any more (already finished by someone else).
func (s *Store) FinishRec(ctx context.Context, id int64, picks []PickRef, now time.Time) (bool, error) {
	if picks == nil {
		picks = []PickRef{}
	}
	b, err := json.Marshal(picks)
	if err != nil {
		return false, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE recommendations SET status = ?, picks = ?, updated_at = ?
		WHERE id = ? AND status = ?`, RecReady, string(b), ts(now), id, RecGenerating)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// MarkNotified remembers that food.recommendation.ready went out.
func (s *Store) MarkNotified(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recommendations SET notified_at = ? WHERE id = ?`, ts(now), id)
	return err
}

// RecsWaiting returns the recommendations waiting for the menu, for days
// from fromDate on.
func (s *Store) RecsWaiting(ctx context.Context, fromDate string) ([]Rec, error) {
	return s.queryRecs(ctx, `SELECT `+recCols+` FROM recommendations WHERE status = ? AND date >= ? ORDER BY id`,
		RecWaitingMenu, fromDate)
}

// RecsWaitingFor returns the recommendations waiting for the menu of one day.
func (s *Store) RecsWaitingFor(ctx context.Context, date string) ([]Rec, error) {
	return s.queryRecs(ctx, `SELECT `+recCols+` FROM recommendations WHERE status = ? AND date = ? ORDER BY id`,
		RecWaitingMenu, date)
}

// RecsGeneratingBefore returns the recommendations that wait for llm since before t.
func (s *Store) RecsGeneratingBefore(ctx context.Context, t time.Time) ([]Rec, error) {
	return s.queryRecs(ctx, `SELECT `+recCols+` FROM recommendations WHERE status = ? AND updated_at < ? ORDER BY id`,
		RecGenerating, ts(t))
}

// RecsUnnotified returns ready recommendations the bot has not been told
// about (the broker was down), for days from fromDate on.
func (s *Store) RecsUnnotified(ctx context.Context, fromDate string) ([]Rec, error) {
	return s.queryRecs(ctx, `SELECT `+recCols+` FROM recommendations
		WHERE status = ? AND (notified_at IS NULL OR notified_at = '') AND date >= ? ORDER BY id`, RecReady, fromDate)
}

// LatestReadyRecsFor returns, per user, the last recommendation of a day
// if it is ready.
func (s *Store) LatestReadyRecsFor(ctx context.Context, date string) ([]Rec, error) {
	return s.queryRecs(ctx, `SELECT `+recCols+` FROM recommendations r
		WHERE date = ? AND status = ? AND id = (SELECT MAX(id) FROM recommendations WHERE user_id = r.user_id AND date = r.date)
		ORDER BY id`, date, RecReady)
}

func (s *Store) queryRecs(ctx context.Context, q string, args ...any) ([]Rec, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rec
	for rows.Next() {
		r, err := scanRec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
