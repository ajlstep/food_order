package org

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
	"food-order/users/internal/profile"
)

// findPerson reads "@nickname" or a phone and returns an active person.
func (s *Service) findPerson(ctx context.Context, q PeopleStore, who string) (string, error) {
	who = strings.TrimSpace(who)
	var (
		id  string
		err error
	)
	if nick, ok := strings.CutPrefix(who, "@"); ok {
		id, err = q.UserByNickname(ctx, nick)
	} else if phone, ok := profile.NormPhone(who); ok {
		id, err = q.UserByPhone(ctx, phone)
	} else {
		return "", fail(users.ErrInvalidInput, "field", "who")
	}
	if errors.Is(err, ErrNotFound) {
		return "", fail(users.ErrUserNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("find person: %w", err)
	}
	status, err := q.AccountStatus(ctx, id)
	if err != nil {
		return "", fmt.Errorf("find person: %w", err)
	}
	if status != statusActive {
		return "", fail(users.ErrUserNotFound)
	}
	return id, nil
}

// InviteToCompany: the owner invites a person.
func (s *Service) InviteToCompany(ctx context.Context, ownerID, who string) error {
	return s.invite(ctx, ownerID, who, func(tx Tx, to string) (Invite, error) {
		c, err := s.ownCompany(ctx, tx, ownerID)
		if err != nil {
			return Invite{}, err
		}
		members, err := tx.CompanyMembers(ctx, c.ID)
		if err != nil {
			return Invite{}, err
		}
		if has(members, to) {
			return Invite{}, fail(users.ErrInviteExists)
		}
		return Invite{Kind: food.OrgCompany, TargetID: c.ID, Name: c.Name}, nil
	})
}

// InviteToGroup: the owner invites a person. A group of a company takes only
// people of that company.
func (s *Service) InviteToGroup(ctx context.Context, ownerID, who string) error {
	return s.invite(ctx, ownerID, who, func(tx Tx, to string) (Invite, error) {
		g, err := s.ownGroup(ctx, tx, ownerID)
		if err != nil {
			return Invite{}, err
		}
		members, err := tx.GroupMembers(ctx, g.ID)
		if err != nil {
			return Invite{}, err
		}
		if has(members, to) {
			return Invite{}, fail(users.ErrInviteExists)
		}
		if err := s.fitsGroup(ctx, tx, g, to); err != nil {
			return Invite{}, err
		}
		return Invite{Kind: food.OrgGroup, TargetID: g.ID, Name: g.Name}, nil
	})
}

// fitsGroup checks the rule of the groups of a company.
func (s *Service) fitsGroup(ctx context.Context, q CompanyStore, g Group, userID string) error {
	if g.CompanyID == "" {
		return nil
	}
	c, err := q.CompanyOf(ctx, userID)
	if errors.Is(err, ErrNotFound) || (err == nil && c.ID != g.CompanyID) {
		return fail(users.ErrOtherCompany)
	}
	return err
}

// invite is what both invitations have in common; target says where to.
func (s *Service) invite(ctx context.Context, from, who string, target func(Tx, string) (Invite, error)) error {
	var out outbox
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, from); err != nil {
			return err
		}
		if err := tx.ExpireInvites(ctx, s.now()); err != nil {
			return err
		}
		to, err := s.findPerson(ctx, tx, who)
		if err != nil {
			return err
		}
		if to == from {
			return fail(users.ErrCannotInviteSelf)
		}
		in, err := target(tx, to)
		if err != nil {
			return err
		}
		in.ID, in.FromUser, in.ToUser = events.NewID(s.now()), from, to
		in.Expires = s.now().Add(inviteTTL)
		err = tx.CreateInvite(ctx, in, s.now())
		if errors.Is(err, ErrConflict) {
			return fail(users.ErrInviteExists)
		}
		if err != nil {
			return err
		}
		out.invites = append(out.invites, food.OrgInvite{UserID: to, InviteID: in.ID, Kind: in.Kind, Name: in.Name})
		return nil
	})
	if err != nil {
		return fmt.Errorf("invite: %w", err)
	}
	s.send(ctx, out)
	return nil
}

// Invites lists the open invitations of the person.
func (s *Service) Invites(ctx context.Context, userID string) ([]Invite, error) {
	if err := s.active(ctx, s.store, userID); err != nil {
		return nil, err
	}
	list, err := s.store.OpenInvites(ctx, userID, s.now())
	if err != nil {
		return nil, fmt.Errorf("load invites: %w", err)
	}
	return list, nil
}

// answerable returns an invitation the person may still answer. Answered,
// expired, and "the company closed meanwhile" look the same to them.
func (s *Service) answerable(ctx context.Context, tx Tx, userID, id string) (Invite, error) {
	in, err := tx.Invite(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && in.ToUser != userID) {
		return Invite{}, fail(users.ErrInviteExpired)
	}
	if err != nil {
		return Invite{}, err
	}
	if in.Status != inviteOpen || !in.Expires.After(s.now()) {
		return Invite{}, fail(users.ErrInviteExpired)
	}
	return in, nil
}

// AcceptInvite puts the person in the company or group.
func (s *Service) AcceptInvite(ctx context.Context, userID, id string) error {
	var out outbox
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		in, err := s.answerable(ctx, tx, userID, id)
		if err != nil {
			return err
		}
		if in.Kind == food.OrgCompany {
			err = s.joinCompany(ctx, tx, in)
		} else {
			err = s.joinGroup(ctx, tx, in)
		}
		if err != nil {
			return err
		}
		out.notice(in.FromUser, food.NoticeAccepted, in.Kind, in.Name)
		return tx.SetInviteStatus(ctx, id, inviteAccepted)
	})
	if err != nil {
		return fmt.Errorf("accept invite: %w", err)
	}
	s.send(ctx, out)
	return nil
}

func (s *Service) joinCompany(ctx context.Context, tx Tx, in Invite) error {
	if _, err := tx.CompanyByID(ctx, in.TargetID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return fail(users.ErrInviteExpired)
		}
		return err
	}
	err := tx.AddCompanyMember(ctx, in.TargetID, in.ToUser, s.now())
	if errors.Is(err, ErrConflict) {
		return fail(users.ErrInCompany)
	}
	return err
}

func (s *Service) joinGroup(ctx context.Context, tx Tx, in Invite) error {
	g, err := tx.GroupByID(ctx, in.TargetID)
	if errors.Is(err, ErrNotFound) {
		return fail(users.ErrInviteExpired)
	}
	if err != nil {
		return err
	}
	if err := s.fitsGroup(ctx, tx, g, in.ToUser); err != nil { // they may have left the company since
		return err
	}
	err = tx.AddGroupMember(ctx, g.ID, in.ToUser, s.now())
	if errors.Is(err, ErrConflict) {
		return fail(users.ErrInGroup)
	}
	return err
}

// DeclineInvite closes the invitation and tells whoever sent it.
func (s *Service) DeclineInvite(ctx context.Context, userID, id string) error {
	var out outbox
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		in, err := s.answerable(ctx, tx, userID, id)
		if err != nil {
			return err
		}
		out.notice(in.FromUser, food.NoticeDeclined, in.Kind, in.Name)
		return tx.SetInviteStatus(ctx, id, inviteDeclined)
	})
	if err != nil {
		return fmt.Errorf("decline invite: %w", err)
	}
	s.send(ctx, out)
	return nil
}
