package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// Order is one saved choice with the snapshot of the dish.
type Order struct {
	ID     int64
	UserID int64
	Date   string
	DishID int64
	Price  *float64
	Rating *int
	// Comment and Labels come with the rating; Labels is raw JSON ("" = none).
	Comment         string
	Labels          string
	KitchenFeedback string
	Forced          bool

	Snapshot

	RatingRequestedAt string
	RatedAt           string
	CreatedAt         string
	UpdatedAt         string
}

// Snapshot is the dish as it was when the user chose it.
type Snapshot struct {
	DishName        string
	PortionG        *float64
	KcalMin         *float64
	KcalMax         *float64
	ProteinGMin     *float64
	ProteinGMax     *float64
	FatGMin         *float64
	FatGMax         *float64
	CarbsGMin       *float64
	CarbsGMax       *float64
	NutritionSource string // llm | kitchen | missing
}

// ErrRated is returned when the user changes a choice that is already rated.
var ErrRated = errors.New("order already rated")

const orderCols = `id, user_id, date, dish_id, price, rating, COALESCE(comment, ''), COALESCE(labels, ''),
	COALESCE(kitchen_feedback, ''), COALESCE(forced, 0),
	dish_name, portion_g, kcal_min, kcal_max, protein_g_min, protein_g_max, fat_g_min, fat_g_max,
	carbs_g_min, carbs_g_max, COALESCE(nutrition_source, 'missing'),
	COALESCE(rating_requested_at, ''), COALESCE(rated_at, ''), created_at, updated_at`

type scanner interface{ Scan(dest ...any) error }

func scanOrder(r scanner) (Order, error) {
	var o Order
	var price, portion, kmin, kmax, pmin, pmax, fmin, fmax, cmin, cmax sql.NullFloat64
	var rating sql.NullInt64
	var forced int
	err := r.Scan(&o.ID, &o.UserID, &o.Date, &o.DishID, &price, &rating, &o.Comment, &o.Labels,
		&o.KitchenFeedback, &forced,
		&o.DishName, &portion, &kmin, &kmax, &pmin, &pmax, &fmin, &fmax, &cmin, &cmax, &o.NutritionSource,
		&o.RatingRequestedAt, &o.RatedAt, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return Order{}, err
	}
	o.Forced = forced != 0
	o.Price = nf(price)
	if rating.Valid {
		r := int(rating.Int64)
		o.Rating = &r
	}
	o.PortionG, o.KcalMin, o.KcalMax = nf(portion), nf(kmin), nf(kmax)
	o.ProteinGMin, o.ProteinGMax = nf(pmin), nf(pmax)
	o.FatGMin, o.FatGMax = nf(fmin), nf(fmax)
	o.CarbsGMin, o.CarbsGMax = nf(cmin), nf(cmax)
	return o, nil
}

func nf(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

func pf(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// SaveOrder saves the choice of a user for a day. There is one order per
// user and day: a second choice replaces the first one, unless the first is
// already rated (ErrRated). It returns the saved order.
func (s *Store) SaveOrder(ctx context.Context, o Order, now time.Time) (Order, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, err
	}
	defer tx.Rollback()

	existing, err := scanOrder(tx.QueryRowContext(ctx,
		`SELECT `+orderCols+` FROM orders WHERE user_id = ? AND date = ? ORDER BY id DESC LIMIT 1`, o.UserID, o.Date))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `INSERT INTO orders (user_id, date, dish_id, price, forced,
			dish_name, portion_g, kcal_min, kcal_max, protein_g_min, protein_g_max, fat_g_min, fat_g_max,
			carbs_g_min, carbs_g_max, nutrition_source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			o.UserID, o.Date, o.DishID, pf(o.Price), boolInt(o.Forced),
			o.DishName, pf(o.PortionG), pf(o.KcalMin), pf(o.KcalMax), pf(o.ProteinGMin), pf(o.ProteinGMax),
			pf(o.FatGMin), pf(o.FatGMax), pf(o.CarbsGMin), pf(o.CarbsGMax), o.NutritionSource, ts(now), ts(now))
		if err != nil {
			return Order{}, err
		}
		o.ID, _ = res.LastInsertId()
	case err != nil:
		return Order{}, err
	case existing.Rating != nil:
		return existing, ErrRated
	default:
		o.ID = existing.ID
		_, err := tx.ExecContext(ctx, `UPDATE orders SET dish_id = ?, price = ?, forced = ?,
			dish_name = ?, portion_g = ?, kcal_min = ?, kcal_max = ?, protein_g_min = ?, protein_g_max = ?,
			fat_g_min = ?, fat_g_max = ?, carbs_g_min = ?, carbs_g_max = ?, nutrition_source = ?,
			comment = NULL, labels = NULL, kitchen_feedback = NULL, rating_requested_at = NULL, updated_at = ?
			WHERE id = ?`,
			o.DishID, pf(o.Price), boolInt(o.Forced),
			o.DishName, pf(o.PortionG), pf(o.KcalMin), pf(o.KcalMax), pf(o.ProteinGMin), pf(o.ProteinGMax),
			pf(o.FatGMin), pf(o.FatGMax), pf(o.CarbsGMin), pf(o.CarbsGMax), o.NutritionSource, ts(now), o.ID)
		if err != nil {
			return Order{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Order{}, err
	}
	return s.Order(ctx, o.UserID, o.ID)
}

// Order returns one order of a user (ErrNotFound if it is not theirs).
func (s *Store) Order(ctx context.Context, userID, id int64) (Order, error) {
	o, err := scanOrder(s.db.QueryRowContext(ctx,
		`SELECT `+orderCols+` FROM orders WHERE id = ? AND user_id = ?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	return o, err
}

// OrderByID returns an order of any user (for events, which carry only order_id).
func (s *Store) OrderByID(ctx context.Context, id int64) (Order, error) {
	o, err := scanOrder(s.db.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	return o, err
}

// Orders returns the orders of a user between two dates (inclusive), oldest first.
func (s *Store) Orders(ctx context.Context, userID int64, from, to string) ([]Order, error) {
	return s.queryOrders(ctx, `SELECT `+orderCols+` FROM orders
		WHERE user_id = ? AND date >= ? AND date <= ? ORDER BY date, id`, userID, from, to)
}

// HasOrder says whether the user already chose something for the day.
func (s *Store) HasOrder(ctx context.Context, userID int64, date string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE user_id = ? AND date = ?`, userID, date).Scan(&n)
	return n > 0, err
}

// Rate saves the rating and comment of an order of the user.
func (s *Store) Rate(ctx context.Context, userID, id int64, rating int, comment string, now time.Time) (Order, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE orders SET rating = ?, comment = ?, labels = NULL,
		kitchen_feedback = NULL, rated_at = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		rating, nullStr(comment), ts(now), ts(now), id, userID)
	if err != nil {
		return Order{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Order{}, ErrNotFound
	}
	return s.Order(ctx, userID, id)
}

// SetLabels saves what llm extracted from the comment.
func (s *Store) SetLabels(ctx context.Context, id int64, labels json.RawMessage, kitchenFeedback string, now time.Time) error {
	var lab any
	if len(labels) > 0 && string(labels) != "null" {
		lab = string(labels)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE orders SET labels = ?, kitchen_feedback = ?, updated_at = ? WHERE id = ?`,
		lab, nullStr(kitchenFeedback), ts(now), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OrdersToAskRating returns the orders of all users between two dates
// (inclusive) that are not rated and were not asked about yet.
func (s *Store) OrdersToAskRating(ctx context.Context, from, to string) ([]Order, error) {
	return s.queryOrders(ctx, `SELECT `+orderCols+` FROM orders
		WHERE rating IS NULL AND rating_requested_at IS NULL AND date >= ? AND date <= ? ORDER BY date, id`, from, to)
}

// MarkRatingRequested remembers that the rating question went out.
func (s *Store) MarkRatingRequested(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE orders SET rating_requested_at = ? WHERE id = ?`, ts(now), id)
	return err
}

func (s *Store) queryOrders(ctx context.Context, q string, args ...any) ([]Order, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
