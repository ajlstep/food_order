package org

import (
	"context"
	"errors"
	"fmt"

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
)

// GroupInput is what is needed to make a group.
type GroupInput struct {
	Name, Address, Notes string
	Lat, Lon             *float64
	Free                 bool // not of the person's company
}

func (in GroupInput) clean() (GroupInput, error) {
	var ok bool
	if in.Name, ok = cleanName(in.Name, maxNameLen); !ok {
		return in, fail(users.ErrInvalidInput, "field", "name")
	}
	if in.Address, ok = cleanName(in.Address, maxAddrLen); !ok {
		return in, fail(users.ErrInvalidInput, "field", "address")
	}
	if (in.Lat == nil) != (in.Lon == nil) {
		return in, fail(users.ErrInvalidInput, "field", "lat")
	}
	if in.Notes != "" {
		if in.Notes, ok = cleanName(in.Notes, maxNoteLen); !ok {
			return in, fail(users.ErrInvalidInput, "field", "notes")
		}
	}
	return in, nil
}

// CreateGroup makes a group with the person as owner and first member. It
// belongs to their company, if they have one and did not ask for a free one.
func (s *Service) CreateGroup(ctx context.Context, userID string, raw GroupInput) (Group, error) {
	in, err := raw.clean()
	if err != nil {
		return Group{}, err
	}
	g := Group{
		ID: events.NewID(s.now()), Name: in.Name, OwnerID: userID,
		Address: in.Address, Lat: in.Lat, Lon: in.Lon, Notes: in.Notes,
	}
	err = s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		if !in.Free {
			c, err := tx.CompanyOf(ctx, userID)
			switch {
			case err == nil:
				g.CompanyID, g.CompanyName = c.ID, c.Name
			case !errors.Is(err, ErrNotFound):
				return err
			}
		}
		if err := tx.CreateGroup(ctx, g, s.now()); err != nil {
			return err
		}
		err := tx.AddGroupMember(ctx, g.ID, userID, s.now())
		if errors.Is(err, ErrConflict) {
			return fail(users.ErrInGroup)
		}
		return err
	})
	if err != nil {
		return Group{}, fmt.Errorf("create group: %w", err)
	}
	s.log.InfoContext(ctx, "group created", "group_id", g.ID, "company_id", g.CompanyID, "user_id", userID)
	return g, nil
}

// groupOf returns the group of the person, or the refusal for having none.
func (s *Service) groupOf(ctx context.Context, q GroupStore, userID string) (Group, error) {
	g, err := q.GroupOf(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return Group{}, fail(users.ErrNotInGroup)
	}
	if err != nil {
		return Group{}, fmt.Errorf("load group: %w", err)
	}
	return g, nil
}

// ownGroup is groupOf for what only the owner may do.
func (s *Service) ownGroup(ctx context.Context, q GroupStore, userID string) (Group, error) {
	g, err := s.groupOf(ctx, q, userID)
	if err == nil && g.OwnerID != userID {
		return Group{}, fail(users.ErrNotOwner)
	}
	return g, err
}

// Group returns the group of the person, with its members.
func (s *Service) Group(ctx context.Context, userID string) (Group, []Member, error) {
	if err := s.active(ctx, s.store, userID); err != nil {
		return Group{}, nil, err
	}
	g, err := s.groupOf(ctx, s.store, userID)
	if err != nil {
		return Group{}, nil, err
	}
	members, err := s.store.GroupMembers(ctx, g.ID)
	if err != nil {
		return Group{}, nil, fmt.Errorf("load members: %w", err)
	}
	return g, members, nil
}

// Snapshot says where an order of this person goes now. No group is not a
// refusal: then the order is personal.
func (s *Service) Snapshot(ctx context.Context, userID string) (Group, error) {
	if err := s.active(ctx, s.store, userID); err != nil {
		return Group{}, err
	}
	g, err := s.store.GroupOf(ctx, userID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Group{}, fmt.Errorf("load group: %w", err)
	}
	return g, nil
}

// LeaveGroup takes the person out of the group.
func (s *Service) LeaveGroup(ctx context.Context, userID string) error {
	var out outbox
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		g, err := s.groupOf(ctx, tx, userID)
		if err != nil {
			return err
		}
		return s.dropFromGroup(ctx, tx, g, userID, &out)
	})
	if err != nil {
		return fmt.Errorf("leave group: %w", err)
	}
	s.send(ctx, out)
	return nil
}

// RemoveFromGroup: the owner takes a member out.
func (s *Service) RemoveFromGroup(ctx context.Context, ownerID, userID string) error {
	var out outbox
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, ownerID); err != nil {
			return err
		}
		g, err := s.ownGroup(ctx, tx, ownerID)
		if err != nil {
			return err
		}
		members, err := tx.GroupMembers(ctx, g.ID)
		if err != nil {
			return err
		}
		if userID == ownerID || !has(members, userID) {
			return fail(users.ErrUserNotFound)
		}
		if err := tx.RemoveGroupMember(ctx, g.ID, userID); err != nil {
			return err
		}
		out.notice(userID, food.NoticeRemoved, food.OrgGroup, g.Name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("remove from group: %w", err)
	}
	s.send(ctx, out)
	return nil
}

// DeleteGroup: the owner closes the group; the members get a message.
func (s *Service) DeleteGroup(ctx context.Context, userID string) error {
	var out outbox
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		g, err := s.ownGroup(ctx, tx, userID)
		if err != nil {
			return err
		}
		members, err := tx.GroupMembers(ctx, g.ID)
		if err != nil {
			return err
		}
		for _, m := range members {
			if err := tx.RemoveGroupMember(ctx, g.ID, m.UserID); err != nil {
				return err
			}
			if m.UserID != userID {
				out.notice(m.UserID, food.NoticeRemoved, food.OrgGroup, g.Name)
			}
		}
		return tx.CloseGroup(ctx, g.ID, s.now())
	})
	if err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	s.send(ctx, out)
	return nil
}

// dropFromGroup takes a person out. A group always has an owner: when the
// owner goes, the oldest member takes over; the last one closes it.
func (s *Service) dropFromGroup(ctx context.Context, tx Tx, g Group, userID string, out *outbox) error {
	if err := tx.RemoveGroupMember(ctx, g.ID, userID); err != nil {
		return err
	}
	left, err := tx.GroupMembers(ctx, g.ID)
	if err != nil {
		return err
	}
	switch {
	case len(left) == 0:
		return tx.CloseGroup(ctx, g.ID, s.now())
	case g.OwnerID == userID:
		out.notice(left[0].UserID, food.NoticeOwner, food.OrgGroup, g.Name)
		return tx.SetGroupOwner(ctx, g.ID, left[0].UserID)
	}
	out.notice(g.OwnerID, food.NoticeLeft, food.OrgGroup, g.Name)
	return nil
}
