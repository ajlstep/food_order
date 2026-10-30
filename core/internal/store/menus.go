package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Menu request statuses.
const (
	MenuRequested = "requested" // sent, waiting for food.menu.ready or food.menu.error
	MenuReady     = "ready"     // the menu is in catalog
	MenuError     = "error"     // the source answered with an error
	MenuExpired   = "expired"   // no answer in time (treated as source_unavailable)
)

// MenuRequest is one request for the (shared) menu of a period.
type MenuRequest struct {
	RequestID    string
	DateFrom     string
	DateTo       string
	Status       string
	ErrorCode    string
	ErrorMessage string
	Attempts     int    // how many times it was sent
	RequestedAt  string // last time it was sent
	UpdatedAt    string
	NextRetryAt  string // "" = no more retries
	Alerted      bool
}

// Active says whether the request still goes on (sent, or waiting to be resent).
func (m MenuRequest) Active() bool {
	return m.Status == MenuRequested || m.Status != MenuReady && m.NextRetryAt != ""
}

const menuCols = `request_id, date_from, date_to, status, COALESCE(error_code, ''), COALESCE(error_message, ''),
	COALESCE(attempts, 0), COALESCE(requested_at, ''), COALESCE(updated_at, ''), COALESCE(next_retry_at, ''),
	COALESCE(alerted, 0)`

func scanMenu(r scanner) (MenuRequest, error) {
	var m MenuRequest
	var alerted int
	err := r.Scan(&m.RequestID, &m.DateFrom, &m.DateTo, &m.Status, &m.ErrorCode, &m.ErrorMessage,
		&m.Attempts, &m.RequestedAt, &m.UpdatedAt, &m.NextRetryAt, &alerted)
	m.Alerted = alerted != 0
	return m, err
}

// CreateMenuRequest saves a request that is about to be sent.
func (s *Store) CreateMenuRequest(ctx context.Context, m MenuRequest, now time.Time) error {
	if m.Status == "" {
		m.Status = MenuRequested
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO menu_requests (request_id, date_from, date_to, status,
		error_code, error_message, attempts, requested_at, updated_at, next_retry_at, alerted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.RequestID, m.DateFrom, m.DateTo, m.Status, nullStr(m.ErrorCode), nullStr(m.ErrorMessage),
		m.Attempts, nullStr(m.RequestedAt), ts(now), nullStr(m.NextRetryAt), boolInt(m.Alerted))
	return err
}

// UpdateMenuRequest writes back every changing field of a request.
func (s *Store) UpdateMenuRequest(ctx context.Context, m MenuRequest, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE menu_requests SET date_from = ?, date_to = ?, status = ?,
		error_code = ?, error_message = ?, attempts = ?, requested_at = ?, updated_at = ?, next_retry_at = ?, alerted = ?
		WHERE request_id = ?`,
		m.DateFrom, m.DateTo, m.Status, nullStr(m.ErrorCode), nullStr(m.ErrorMessage), m.Attempts,
		nullStr(m.RequestedAt), ts(now), nullStr(m.NextRetryAt), boolInt(m.Alerted), m.RequestID)
	return err
}

// MenuRequestByID returns one request.
func (s *Store) MenuRequestByID(ctx context.Context, requestID string) (MenuRequest, error) {
	m, err := scanMenu(s.db.QueryRowContext(ctx, `SELECT `+menuCols+` FROM menu_requests WHERE request_id = ?`, requestID))
	if errors.Is(err, sql.ErrNoRows) {
		return MenuRequest{}, ErrNotFound
	}
	return m, err
}

// LatestMenuFor returns the last request whose period covers the day.
func (s *Store) LatestMenuFor(ctx context.Context, date string) (MenuRequest, error) {
	m, err := scanMenu(s.db.QueryRowContext(ctx, `SELECT `+menuCols+` FROM menu_requests
		WHERE date_from <= ? AND date_to >= ? ORDER BY rowid DESC LIMIT 1`, date, date))
	if errors.Is(err, sql.ErrNoRows) {
		return MenuRequest{}, ErrNotFound
	}
	return m, err
}

// MenuReadyFor says whether some request covering the day is ready
// (optionally not counting one request id).
func (s *Store) MenuReadyFor(ctx context.Context, date, exceptRequestID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM menu_requests
		WHERE status = ? AND date_from <= ? AND date_to >= ? AND request_id <> ?`,
		MenuReady, date, date, exceptRequestID).Scan(&n)
	return n > 0, err
}

// MenusToRetry returns the requests whose next retry is due.
func (s *Store) MenusToRetry(ctx context.Context, now time.Time) ([]MenuRequest, error) {
	return s.queryMenus(ctx, `SELECT `+menuCols+` FROM menu_requests
		WHERE status IN (?, ?) AND next_retry_at IS NOT NULL AND next_retry_at <> '' AND next_retry_at <= ?
		ORDER BY rowid`, MenuError, MenuExpired, ts(now))
}

// MenusSentBefore returns the requests sent before t and still without an answer.
func (s *Store) MenusSentBefore(ctx context.Context, t time.Time) ([]MenuRequest, error) {
	return s.queryMenus(ctx, `SELECT `+menuCols+` FROM menu_requests
		WHERE status = ? AND (requested_at IS NULL OR requested_at < ?) ORDER BY rowid`, MenuRequested, ts(t))
}

func (s *Store) queryMenus(ctx context.Context, q string, args ...any) ([]MenuRequest, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MenuRequest
	for rows.Next() {
		m, err := scanMenu(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
