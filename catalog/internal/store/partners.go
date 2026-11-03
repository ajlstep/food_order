package store

import (
	"context"
	"fmt"
)

// Partner is one kitchen with its schedule.
type Partner struct {
	ID       int64
	Key      string
	Name     string
	Source   string // the "source" of its menus
	Schedule []ScheduleDay
	Closed   []Closure
}

// ScheduleDay says how orders work for meals on one weekday.
type ScheduleDay struct {
	Weekday      int // ISO, 1 = Monday
	DaysBefore   int // order this many days before the meal
	OrderUntil   string
	DeliveryFrom string
	DeliveryTo   string
}

// Closure is a day the partner does not work.
type Closure struct {
	Date   string
	Reason string
}

// SavePartners makes the database say what the list says: every partner is
// added or changed, its schedule replaced, and the partners that are gone
// are removed. Their old menus stay: a dish only loses its partner.
func (s *Store) SavePartners(ctx context.Context, list []Partner) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM partners`); err != nil {
		return err
	}
	for _, p := range list {
		res, err := tx.ExecContext(ctx, `INSERT INTO partners (key, name, source) VALUES (?, ?, ?)`, p.Key, p.Name, p.Source)
		if err != nil {
			return fmt.Errorf("partner %s: %w", p.Key, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, sc := range p.Schedule {
			if _, err := tx.ExecContext(ctx, `INSERT INTO partner_schedule
				(partner_id, weekday, days_before, order_until, delivery_from, delivery_to) VALUES (?, ?, ?, ?, ?, ?)`,
				id, sc.Weekday, sc.DaysBefore, sc.OrderUntil, sc.DeliveryFrom, sc.DeliveryTo); err != nil {
				return fmt.Errorf("partner %s, day %d: %w", p.Key, sc.Weekday, err)
			}
		}
		for _, c := range p.Closed {
			if _, err := tx.ExecContext(ctx, `INSERT INTO partner_closures (partner_id, date, reason) VALUES (?, ?, ?)`,
				id, c.Date, c.Reason); err != nil {
				return fmt.Errorf("partner %s, %s: %w", p.Key, c.Date, err)
			}
		}
	}
	return tx.Commit()
}

// Partners returns every partner with its schedule, by id.
func (s *Store) Partners(ctx context.Context) ([]Partner, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, key, name, source FROM partners ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []Partner
	for rows.Next() {
		var p Partner
		if err := rows.Scan(&p.ID, &p.Key, &p.Name, &p.Source); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.fillPartner(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// fillPartner reads the schedule and the closures of a partner. It runs
// after the partners' rows are closed: the pool has one connection.
func (s *Store) fillPartner(ctx context.Context, p *Partner) error {
	rows, err := s.db.QueryContext(ctx, `SELECT weekday, days_before, order_until, delivery_from, delivery_to
		FROM partner_schedule WHERE partner_id = ? ORDER BY weekday`, p.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sc ScheduleDay
		if err := rows.Scan(&sc.Weekday, &sc.DaysBefore, &sc.OrderUntil, &sc.DeliveryFrom, &sc.DeliveryTo); err != nil {
			return err
		}
		p.Schedule = append(p.Schedule, sc)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	closures, err := s.db.QueryContext(ctx, `SELECT date, reason FROM partner_closures WHERE partner_id = ? ORDER BY date`, p.ID)
	if err != nil {
		return err
	}
	defer closures.Close()
	for closures.Next() {
		var c Closure
		if err := closures.Scan(&c.Date, &c.Reason); err != nil {
			return err
		}
		p.Closed = append(p.Closed, c)
	}
	return closures.Err()
}
