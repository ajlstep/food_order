package org

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

// oneRow turns "no row" into ErrNotFound.
func oneRow(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// conflict turns a UNIQUE violation into ErrConflict.
func conflict(err error) error {
	if db.IsUnique(err) {
		return ErrConflict
	}
	return err
}

// ---------------------------------------------------------------- people

// AccountStatus implements PeopleStore.
func (s *SQLStore) AccountStatus(ctx context.Context, userID string) (string, error) {
	var status string
	err := s.q.QueryRowContext(ctx, `SELECT status FROM users WHERE id = ?`, userID).Scan(&status)
	return status, oneRow(err)
}

// UserByNickname implements PeopleStore.
func (s *SQLStore) UserByNickname(ctx context.Context, nickname string) (string, error) {
	var id string
	err := s.q.QueryRowContext(ctx,
		`SELECT user_id FROM identities WHERE lower(nickname) = lower(?) AND nickname <> ''
		 ORDER BY last_seen_at DESC, id DESC LIMIT 1`, nickname).Scan(&id)
	return id, oneRow(err)
}

// UserByPhone implements PeopleStore.
func (s *SQLStore) UserByPhone(ctx context.Context, phone string) (string, error) {
	var id string
	err := s.q.QueryRowContext(ctx, `SELECT id FROM users WHERE phone = ?`, phone).Scan(&id)
	return id, oneRow(err)
}

// memberColumns: the name of the account, and the nickname seen last.
const memberColumns = `u.id, u.display_name,
	COALESCE((SELECT i.nickname FROM identities i WHERE i.user_id = u.id AND i.nickname <> ''
	          ORDER BY i.last_seen_at DESC, i.id DESC LIMIT 1), ''), m.joined_at`

func (s *SQLStore) members(ctx context.Context, query, id string) ([]Member, error) {
	rows, err := s.q.QueryContext(ctx, query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var (
			m      Member
			joined int64
		)
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Nickname, &joined); err != nil {
			return nil, err
		}
		m.Joined = db.Time(joined)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- companies

const companyColumns = `c.id, c.name, c.owner_id, c.max_delivery_fee`

func scanCompany(row *sql.Row) (Company, error) {
	var (
		c   Company
		fee sql.NullInt64
	)
	if err := row.Scan(&c.ID, &c.Name, &c.OwnerID, &fee); err != nil {
		return Company{}, oneRow(err)
	}
	if fee.Valid {
		c.MaxFee = &fee.Int64
	}
	return c, nil
}

// CompanyOf implements CompanyStore.
func (s *SQLStore) CompanyOf(ctx context.Context, userID string) (Company, error) {
	return scanCompany(s.q.QueryRowContext(ctx,
		`SELECT `+companyColumns+` FROM companies c JOIN company_members m ON m.company_id = c.id
		 WHERE m.user_id = ? AND c.deleted_at IS NULL`, userID))
}

// CompanyByID implements CompanyStore.
func (s *SQLStore) CompanyByID(ctx context.Context, id string) (Company, error) {
	return scanCompany(s.q.QueryRowContext(ctx,
		`SELECT `+companyColumns+` FROM companies c WHERE c.id = ? AND c.deleted_at IS NULL`, id))
}

// CreateCompany implements CompanyStore.
func (s *SQLStore) CreateCompany(ctx context.Context, c Company, at time.Time) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO companies (id, name, owner_id, max_delivery_fee, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.OwnerID, c.MaxFee, db.Millis(at))
	return err
}

// SaveCompany implements CompanyStore.
func (s *SQLStore) SaveCompany(ctx context.Context, c Company) error {
	_, err := s.q.ExecContext(ctx, `UPDATE companies SET name = ?, max_delivery_fee = ? WHERE id = ?`, c.Name, c.MaxFee, c.ID)
	return err
}

// CloseCompany implements CompanyStore.
func (s *SQLStore) CloseCompany(ctx context.Context, id string, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE companies SET deleted_at = ? WHERE id = ?`, db.Millis(at), id)
	return err
}

// AddCompanyMember implements CompanyStore.
func (s *SQLStore) AddCompanyMember(ctx context.Context, companyID, userID string, at time.Time) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO company_members (company_id, user_id, joined_at) VALUES (?, ?, ?)`, companyID, userID, db.Millis(at))
	return conflict(err)
}

// RemoveCompanyMember implements CompanyStore.
func (s *SQLStore) RemoveCompanyMember(ctx context.Context, companyID, userID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM company_members WHERE company_id = ? AND user_id = ?`, companyID, userID)
	return err
}

// CompanyMembers implements CompanyStore.
func (s *SQLStore) CompanyMembers(ctx context.Context, companyID string) ([]Member, error) {
	return s.members(ctx, `SELECT `+memberColumns+` FROM company_members m JOIN users u ON u.id = m.user_id
		WHERE m.company_id = ? ORDER BY m.joined_at, m.rowid`, companyID)
}

// ---------------------------------------------------------------- groups

const groupColumns = `g.id, g.name, g.owner_id, COALESCE(g.company_id, ''), COALESCE(c.name, ''), g.address, g.lat, g.lon, g.notes`

func scanGroup(row *sql.Row) (Group, error) {
	var (
		g        Group
		lat, lon sql.NullFloat64
	)
	if err := row.Scan(&g.ID, &g.Name, &g.OwnerID, &g.CompanyID, &g.CompanyName, &g.Address, &lat, &lon, &g.Notes); err != nil {
		return Group{}, oneRow(err)
	}
	if lat.Valid && lon.Valid {
		g.Lat, g.Lon = &lat.Float64, &lon.Float64
	}
	return g, nil
}

// GroupOf implements GroupStore.
func (s *SQLStore) GroupOf(ctx context.Context, userID string) (Group, error) {
	return scanGroup(s.q.QueryRowContext(ctx,
		`SELECT `+groupColumns+` FROM groups g JOIN group_members m ON m.group_id = g.id
		 LEFT JOIN companies c ON c.id = g.company_id
		 WHERE m.user_id = ? AND g.deleted_at IS NULL`, userID))
}

// GroupByID implements GroupStore.
func (s *SQLStore) GroupByID(ctx context.Context, id string) (Group, error) {
	return scanGroup(s.q.QueryRowContext(ctx,
		`SELECT `+groupColumns+` FROM groups g LEFT JOIN companies c ON c.id = g.company_id
		 WHERE g.id = ? AND g.deleted_at IS NULL`, id))
}

// CreateGroup implements GroupStore.
func (s *SQLStore) CreateGroup(ctx context.Context, g Group, at time.Time) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO groups (id, name, owner_id, company_id, address, lat, lon, notes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		g.ID, g.Name, g.OwnerID, db.NullIfEmpty(g.CompanyID), g.Address, g.Lat, g.Lon, g.Notes, db.Millis(at))
	return err
}

// SetGroupOwner implements GroupStore.
func (s *SQLStore) SetGroupOwner(ctx context.Context, groupID, userID string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE groups SET owner_id = ? WHERE id = ?`, userID, groupID)
	return err
}

// CloseGroup implements GroupStore.
func (s *SQLStore) CloseGroup(ctx context.Context, id string, at time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE groups SET deleted_at = ? WHERE id = ?`, db.Millis(at), id)
	return err
}

// AddGroupMember implements GroupStore.
func (s *SQLStore) AddGroupMember(ctx context.Context, groupID, userID string, at time.Time) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO group_members (group_id, user_id, joined_at) VALUES (?, ?, ?)`, groupID, userID, db.Millis(at))
	return conflict(err)
}

// RemoveGroupMember implements GroupStore.
func (s *SQLStore) RemoveGroupMember(ctx context.Context, groupID, userID string) error {
	_, err := s.q.ExecContext(ctx, `DELETE FROM group_members WHERE group_id = ? AND user_id = ?`, groupID, userID)
	return err
}

// GroupMembers implements GroupStore.
func (s *SQLStore) GroupMembers(ctx context.Context, groupID string) ([]Member, error) {
	return s.members(ctx, `SELECT `+memberColumns+` FROM group_members m JOIN users u ON u.id = m.user_id
		WHERE m.group_id = ? ORDER BY m.joined_at, m.rowid`, groupID)
}

// ---------------------------------------------------------------- invitations

// inviteName: the name of the place, also of a closed one.
const inviteName = `COALESCE((SELECT c.name FROM companies c WHERE i.kind = 'company' AND c.id = i.target_id),
	(SELECT g.name FROM groups g WHERE i.kind = 'group' AND g.id = i.target_id), '')`

const inviteColumns = `i.id, i.kind, i.target_id, ` + inviteName + `, i.from_user, i.to_user, i.status, i.expires_at`

func scanInvite(scan func(...any) error) (Invite, error) {
	var (
		in      Invite
		expires int64
	)
	if err := scan(&in.ID, &in.Kind, &in.TargetID, &in.Name, &in.FromUser, &in.ToUser, &in.Status, &expires); err != nil {
		return Invite{}, err
	}
	in.Expires = db.Time(expires)
	return in, nil
}

// CreateInvite implements InviteStore.
func (s *SQLStore) CreateInvite(ctx context.Context, i Invite, at time.Time) error {
	_, err := s.q.ExecContext(ctx,
		`INSERT INTO invites (id, kind, target_id, from_user, to_user, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		i.ID, i.Kind, i.TargetID, i.FromUser, i.ToUser, db.Millis(i.Expires), db.Millis(at))
	return conflict(err)
}

// Invite implements InviteStore.
func (s *SQLStore) Invite(ctx context.Context, id string) (Invite, error) {
	in, err := scanInvite(s.q.QueryRowContext(ctx, `SELECT `+inviteColumns+` FROM invites i WHERE i.id = ?`, id).Scan)
	return in, oneRow(err)
}

// OpenInvites implements InviteStore. An invitation to a place that closed
// meanwhile is not shown.
func (s *SQLStore) OpenInvites(ctx context.Context, userID string, now time.Time) ([]Invite, error) {
	rows, err := s.q.QueryContext(ctx, `SELECT `+inviteColumns+` FROM invites i
		WHERE i.to_user = ? AND i.status = 'open' AND i.expires_at > ?
		  AND (EXISTS (SELECT 1 FROM companies c WHERE i.kind = 'company' AND c.id = i.target_id AND c.deleted_at IS NULL)
		    OR EXISTS (SELECT 1 FROM groups g WHERE i.kind = 'group' AND g.id = i.target_id AND g.deleted_at IS NULL))
		ORDER BY i.created_at, i.id`, userID, db.Millis(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Invite
	for rows.Next() {
		in, err := scanInvite(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// SetInviteStatus implements InviteStore.
func (s *SQLStore) SetInviteStatus(ctx context.Context, id, status string) error {
	_, err := s.q.ExecContext(ctx, `UPDATE invites SET status = ? WHERE id = ?`, status, id)
	return err
}

// ExpireInvites implements InviteStore.
func (s *SQLStore) ExpireInvites(ctx context.Context, now time.Time) error {
	_, err := s.q.ExecContext(ctx, `UPDATE invites SET status = 'expired' WHERE status = 'open' AND expires_at <= ?`, db.Millis(now))
	return err
}
