package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Groups: users of the same company together. Rules:
//   - a user is in at most one group;
//   - whoever creates a group is its owner and first member;
//   - you join only by accepting an invitation from the owner;
//   - the owner can remove members; anybody can leave;
//   - when the owner leaves, the oldest member becomes owner;
//   - a group with no members left is deleted (with its invitations).

// Group errors.
var (
	ErrNoGroup           = errors.New("not in a group")
	ErrAlreadyInGroup    = errors.New("already in a group")
	ErrNotOwner          = errors.New("only the owner can do this")
	ErrAlreadyMember     = errors.New("already a member of this group")
	ErrInvitePending     = errors.New("an invitation is already waiting")
	ErrInviteGone        = errors.New("invitation not found, answered or expired")
	ErrCannotRemoveOwner = errors.New("the owner cannot be removed")
	ErrNotMember         = errors.New("not a member of this group")
	ErrBadGroupName      = errors.New("group name must have 2-64 characters")
)

// InviteTTL: an invitation not answered in this time expires.
const InviteTTL = 7 * 24 * time.Hour

// Group is one group (company).
type Group struct {
	ID        int64
	Name      string
	OwnerID   int64
	CreatedAt time.Time
}

// Member is a user in a group.
type Member struct {
	User     User
	JoinedAt time.Time
}

// Invite is one invitation.
type Invite struct {
	ID        int64
	GroupID   int64
	UserID    int64 // invited
	InvitedBy int64
	Status    string
	CreatedAt time.Time
}

// LeaveResult says what happened when somebody left (or was removed).
type LeaveResult struct {
	Group    Group
	Deleted  bool  // no member left: the group is gone
	NewOwner *User // the owner left: this member is the owner now
}

// CleanGroupName trims the name and checks its length.
func CleanGroupName(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if n := utf8.RuneCountInString(name); n < 2 || n > 64 {
		return "", ErrBadGroupName
	}
	return name, nil
}

func parseTS(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func scanGroup(row interface{ Scan(...any) error }) (Group, error) {
	var g Group
	var created string
	err := row.Scan(&g.ID, &g.Name, &g.OwnerID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNoGroup
	}
	g.CreatedAt = parseTS(created)
	return g, err
}

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func groupOf(ctx context.Context, q querier, userID int64) (Group, error) {
	return scanGroup(q.QueryRowContext(ctx, `
		SELECT g.id, g.name, g.owner_id, g.created_at
		FROM groups g JOIN group_members m ON m.group_id = g.id
		WHERE m.user_id = ?`, userID))
}

// GroupOf returns the group of a user, or ErrNoGroup.
func (s *Store) GroupOf(ctx context.Context, userID int64) (Group, error) {
	return groupOf(ctx, s.db, userID)
}

// GroupByID returns a group, or ErrNoGroup.
func (s *Store) GroupByID(ctx context.Context, id int64) (Group, error) {
	return scanGroup(s.db.QueryRowContext(ctx, `SELECT id, name, owner_id, created_at FROM groups WHERE id = ?`, id))
}

// CreateGroup makes a new group with the user as owner and first member.
func (s *Store) CreateGroup(ctx context.Context, ownerID int64, name string) (Group, error) {
	name, err := CleanGroupName(name)
	if err != nil {
		return Group{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Group{}, err
	}
	defer tx.Rollback()

	if _, err := groupOf(ctx, tx, ownerID); err == nil {
		return Group{}, ErrAlreadyInGroup
	} else if !errors.Is(err, ErrNoGroup) {
		return Group{}, err
	}
	now := s.ts()
	res, err := tx.ExecContext(ctx, `INSERT INTO groups (name, owner_id, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		name, ownerID, now, now)
	if err != nil {
		return Group{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO group_members (group_id, user_id, joined_at) VALUES (?, ?, ?)`,
		id, ownerID, now); err != nil {
		return Group{}, err
	}
	if err := tx.Commit(); err != nil {
		return Group{}, err
	}
	return Group{ID: id, Name: name, OwnerID: ownerID, CreatedAt: parseTS(now)}, nil
}

// RenameGroup changes the name (owner only).
func (s *Store) RenameGroup(ctx context.Context, groupID, ownerID int64, name string) (string, error) {
	name, err := CleanGroupName(name)
	if err != nil {
		return "", err
	}
	g, err := s.GroupByID(ctx, groupID)
	if err != nil {
		return "", err
	}
	if g.OwnerID != ownerID {
		return "", ErrNotOwner
	}
	_, err = s.db.ExecContext(ctx, `UPDATE groups SET name = ?, updated_at = ? WHERE id = ?`, name, s.ts(), groupID)
	return name, err
}

// Members returns the members, the oldest first.
func (s *Store) Members(ctx context.Context, groupID int64) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT u.id, u.chat_id, COALESCE(u.tg_user_id, 0), u.first_name, u.last_name, u.username, u.phone,
		       u.language_code, u.status, u.created_at, u.updated_at, m.joined_at
		FROM group_members m JOIN users u ON u.id = m.user_id
		WHERE m.group_id = ? ORDER BY m.joined_at, u.id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		var created, updated, joined string
		err := rows.Scan(&m.User.ID, &m.User.ChatID, &m.User.TGUserID, &m.User.FirstName, &m.User.LastName,
			&m.User.Username, &m.User.Phone, &m.User.LanguageCode, &m.User.Status, &created, &updated, &joined)
		if err != nil {
			return nil, err
		}
		m.User.CreatedAt, m.User.UpdatedAt, m.JoinedAt = parseTS(created), parseTS(updated), parseTS(joined)
		out = append(out, m)
	}
	return out, rows.Err()
}

// FindUser finds a registered user by @nickname or by phone number.
func (s *Store) FindUser(ctx context.Context, query string) (User, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return User{}, ErrNotFound
	}
	if digits := NormalizePhone(q); digits != "" {
		return scanUser(s.db.QueryRowContext(ctx, `
			SELECT `+userCols+` FROM users
			WHERE phone != '' AND REPLACE(REPLACE(REPLACE(phone, '+', ''), ' ', ''), '-', '') = ?
			ORDER BY id LIMIT 1`, digits))
	}
	nick := strings.TrimPrefix(q, "@")
	if nick == "" {
		return User{}, ErrNotFound
	}
	return scanUser(s.db.QueryRowContext(ctx, `
		SELECT `+userCols+` FROM users WHERE username != '' AND LOWER(username) = LOWER(?) ORDER BY id LIMIT 1`, nick))
}

// NormalizePhone keeps the digits of a phone number ("+373 69-000-000" ->
// "37369000000"). A local Moldovan number (069000000) gets the 373 prefix.
// It returns "" if the text does not look like a phone number.
func NormalizePhone(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '(' || r == ')':
		default:
			return "" // letters: a nickname, not a phone
		}
	}
	d := b.String()
	d = strings.TrimPrefix(d, "00")
	if len(d) == 9 && d[0] == '0' {
		d = "373" + d[1:]
	}
	if len(d) < 8 {
		return ""
	}
	return d
}

// CreateInvite invites a user into a group (owner only).
func (s *Store) CreateInvite(ctx context.Context, groupID, ownerID, userID int64) (Invite, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invite{}, err
	}
	defer tx.Rollback()

	var owner int64
	if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM groups WHERE id = ?`, groupID).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Invite{}, ErrNoGroup
		}
		return Invite{}, err
	}
	if owner != ownerID {
		return Invite{}, ErrNotOwner
	}
	if g, err := groupOf(ctx, tx, userID); err == nil && g.ID == groupID {
		return Invite{}, ErrAlreadyMember
	}
	now := s.ts()
	if _, err := tx.ExecContext(ctx, `UPDATE group_invites SET status = 'expired', updated_at = ?
		WHERE status = 'pending' AND created_at < ?`, now, s.now().Add(-InviteTTL).UTC().Format(time.RFC3339)); err != nil {
		return Invite{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO group_invites (group_id, user_id, invited_by, status, created_at, updated_at)
		VALUES (?, ?, ?, 'pending', ?, ?) ON CONFLICT DO NOTHING`, groupID, userID, ownerID, now, now)
	if err != nil {
		return Invite{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Invite{}, ErrInvitePending
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return Invite{}, err
	}
	return Invite{ID: id, GroupID: groupID, UserID: userID, InvitedBy: ownerID, Status: "pending", CreatedAt: parseTS(now)}, nil
}

// AnswerInvite accepts or declines an invitation. Only the invited user can
// answer. Accepting fails with ErrAlreadyInGroup if the user is in a group.
func (s *Store) AnswerInvite(ctx context.Context, inviteID, userID int64, accept bool) (Invite, Group, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Invite{}, Group{}, err
	}
	defer tx.Rollback()

	var inv Invite
	var created string
	err = tx.QueryRowContext(ctx, `SELECT id, group_id, user_id, invited_by, status, created_at FROM group_invites WHERE id = ?`,
		inviteID).Scan(&inv.ID, &inv.GroupID, &inv.UserID, &inv.InvitedBy, &inv.Status, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return inv, Group{}, ErrInviteGone
	}
	if err != nil {
		return inv, Group{}, err
	}
	inv.CreatedAt = parseTS(created)
	if inv.UserID != userID || inv.Status != "pending" || s.now().Sub(inv.CreatedAt) > InviteTTL {
		return inv, Group{}, ErrInviteGone
	}
	g, err := scanGroup(tx.QueryRowContext(ctx, `SELECT id, name, owner_id, created_at FROM groups WHERE id = ?`, inv.GroupID))
	if err != nil {
		return inv, g, ErrInviteGone
	}
	status := "declined"
	if accept {
		if cur, err := groupOf(ctx, tx, userID); err == nil {
			if cur.ID == g.ID {
				status = "accepted" // already in: just close the invitation
			} else {
				return inv, cur, ErrAlreadyInGroup
			}
		} else if !errors.Is(err, ErrNoGroup) {
			return inv, g, err
		} else {
			if _, err := tx.ExecContext(ctx, `INSERT INTO group_members (group_id, user_id, joined_at) VALUES (?, ?, ?)`,
				g.ID, userID, s.ts()); err != nil {
				return inv, g, err
			}
			status = "accepted"
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE group_invites SET status = ?, updated_at = ? WHERE id = ?`, status, s.ts(), inv.ID); err != nil {
		return inv, g, err
	}
	inv.Status = status
	return inv, g, tx.Commit()
}

// RemoveMember takes a member out of the group (owner only).
func (s *Store) RemoveMember(ctx context.Context, groupID, ownerID, userID int64) (LeaveResult, error) {
	g, err := s.GroupByID(ctx, groupID)
	if err != nil {
		return LeaveResult{}, err
	}
	if g.OwnerID != ownerID {
		return LeaveResult{}, ErrNotOwner
	}
	if userID == ownerID {
		return LeaveResult{}, ErrCannotRemoveOwner
	}
	cur, err := s.GroupOf(ctx, userID)
	if err != nil || cur.ID != groupID {
		return LeaveResult{}, ErrNotMember
	}
	return s.leave(ctx, userID)
}

// LeaveGroup takes the user out of its group. The last one out deletes the
// group; if the owner leaves, the oldest member becomes owner.
func (s *Store) LeaveGroup(ctx context.Context, userID int64) (LeaveResult, error) {
	return s.leave(ctx, userID)
}

func (s *Store) leave(ctx context.Context, userID int64) (LeaveResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LeaveResult{}, err
	}
	defer tx.Rollback()

	g, err := groupOf(ctx, tx, userID)
	if err != nil {
		return LeaveResult{}, err
	}
	res := LeaveResult{Group: g}
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_members WHERE group_id = ? AND user_id = ?`, g.ID, userID); err != nil {
		return res, err
	}

	var next int64
	err = tx.QueryRowContext(ctx, `SELECT user_id FROM group_members WHERE group_id = ? ORDER BY joined_at, user_id LIMIT 1`,
		g.ID).Scan(&next)
	switch {
	case errors.Is(err, sql.ErrNoRows): // nobody left: delete the group (invitations go with it)
		if _, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, g.ID); err != nil {
			return res, err
		}
		res.Deleted = true
	case err != nil:
		return res, err
	case g.OwnerID == userID: // the owner left: hand the group over
		if _, err := tx.ExecContext(ctx, `UPDATE groups SET owner_id = ?, updated_at = ? WHERE id = ?`, next, s.ts(), g.ID); err != nil {
			return res, err
		}
		u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, next))
		if err != nil {
			return res, err
		}
		res.NewOwner = &u
		res.Group.OwnerID = next
	}
	return res, tx.Commit()
}
