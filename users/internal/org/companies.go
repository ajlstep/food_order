package org

import (
	"context"
	"errors"
	"fmt"

	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
	"food-order/shared/events"
)

// CreateCompany makes a company with the person as owner and first member.
func (s *Service) CreateCompany(ctx context.Context, userID, rawName string) (Company, error) {
	name, ok := cleanName(rawName, maxNameLen)
	if !ok {
		return Company{}, fail(users.ErrInvalidInput, "field", "name")
	}
	c := Company{ID: events.NewID(s.now()), Name: name, OwnerID: userID}
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		if err := tx.CreateCompany(ctx, c, s.now()); err != nil {
			return err
		}
		err := tx.AddCompanyMember(ctx, c.ID, userID, s.now())
		if errors.Is(err, ErrConflict) {
			return fail(users.ErrInCompany)
		}
		return err
	})
	if err != nil {
		return Company{}, fmt.Errorf("create company: %w", err)
	}
	s.log.InfoContext(ctx, "company created", "company_id", c.ID, "user_id", userID)
	return c, nil
}

// companyOf returns the company of the person, or the refusal for having none.
func (s *Service) companyOf(ctx context.Context, q CompanyStore, userID string) (Company, error) {
	c, err := q.CompanyOf(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return Company{}, fail(users.ErrNotInCompany)
	}
	if err != nil {
		return Company{}, fmt.Errorf("load company: %w", err)
	}
	return c, nil
}

// ownCompany is companyOf for what only the owner may do.
func (s *Service) ownCompany(ctx context.Context, q CompanyStore, userID string) (Company, error) {
	c, err := s.companyOf(ctx, q, userID)
	if err == nil && c.OwnerID != userID {
		return Company{}, fail(users.ErrNotOwner)
	}
	return c, err
}

// CompanyView is the company as a person sees it: the members come only for
// the owner, the others see how many they are.
type CompanyView struct {
	Company
	IsOwner     bool
	MemberCount int
	Members     []Member
}

// Company returns the company of the person.
func (s *Service) Company(ctx context.Context, userID string) (CompanyView, error) {
	if err := s.active(ctx, s.store, userID); err != nil {
		return CompanyView{}, err
	}
	c, err := s.companyOf(ctx, s.store, userID)
	if err != nil {
		return CompanyView{}, err
	}
	members, err := s.store.CompanyMembers(ctx, c.ID)
	if err != nil {
		return CompanyView{}, fmt.Errorf("load members: %w", err)
	}
	v := CompanyView{Company: c, IsOwner: c.OwnerID == userID, MemberCount: len(members)}
	if v.IsOwner {
		v.Members = members
	}
	return v, nil
}

// CompanyChange says what to change; nil means "leave as it is".
type CompanyChange struct {
	Name     *string
	MaxFee   *int64
	ClearFee bool
}

// UpdateCompany changes the name or the limit of the delivery fee.
func (s *Service) UpdateCompany(ctx context.Context, userID string, ch CompanyChange) (CompanyView, error) {
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		c, err := s.ownCompany(ctx, tx, userID)
		if err != nil {
			return err
		}
		if ch.Name != nil {
			name, ok := cleanName(*ch.Name, maxNameLen)
			if !ok {
				return fail(users.ErrInvalidInput, "field", "name")
			}
			c.Name = name
		}
		if ch.MaxFee != nil {
			if *ch.MaxFee < 0 {
				return fail(users.ErrInvalidInput, "field", "max_delivery_fee")
			}
			c.MaxFee = ch.MaxFee
		}
		if ch.ClearFee {
			c.MaxFee = nil
		}
		return tx.SaveCompany(ctx, c)
	})
	if err != nil {
		return CompanyView{}, fmt.Errorf("update company: %w", err)
	}
	return s.Company(ctx, userID)
}

// LeaveCompany takes the person out, and out of the group of the company.
// The owner cannot leave people without an owner; alone, they close it.
func (s *Service) LeaveCompany(ctx context.Context, userID string) error {
	var out outbox
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, userID); err != nil {
			return err
		}
		c, err := s.companyOf(ctx, tx, userID)
		if err != nil {
			return err
		}
		members, err := tx.CompanyMembers(ctx, c.ID)
		if err != nil {
			return err
		}
		isOwner := c.OwnerID == userID
		if isOwner && len(members) > 1 {
			return fail(users.ErrOwnerStays)
		}
		if err := s.dropFromCompany(ctx, tx, c, userID, &out); err != nil {
			return err
		}
		if isOwner {
			return tx.CloseCompany(ctx, c.ID, s.now())
		}
		out.notice(c.OwnerID, food.NoticeLeft, food.OrgCompany, c.Name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("leave company: %w", err)
	}
	s.send(ctx, out)
	return nil
}

// RemoveFromCompany: the owner takes a member out.
func (s *Service) RemoveFromCompany(ctx context.Context, ownerID, userID string) error {
	var out outbox
	err := s.store.InTx(ctx, func(tx Tx) error {
		if err := s.active(ctx, tx, ownerID); err != nil {
			return err
		}
		c, err := s.ownCompany(ctx, tx, ownerID)
		if err != nil {
			return err
		}
		if userID == ownerID {
			return fail(users.ErrOwnerStays)
		}
		members, err := tx.CompanyMembers(ctx, c.ID)
		if err != nil {
			return err
		}
		if !has(members, userID) {
			return fail(users.ErrUserNotFound)
		}
		if err := s.dropFromCompany(ctx, tx, c, userID, &out); err != nil {
			return err
		}
		out.notice(userID, food.NoticeRemoved, food.OrgCompany, c.Name)
		return nil
	})
	if err != nil {
		return fmt.Errorf("remove from company: %w", err)
	}
	s.send(ctx, out)
	return nil
}

// dropFromCompany takes a person out of a company and of its group: a group
// of a company has only people of that company.
func (s *Service) dropFromCompany(ctx context.Context, tx Tx, c Company, userID string, out *outbox) error {
	g, err := tx.GroupOf(ctx, userID)
	switch {
	case err == nil && g.CompanyID == c.ID:
		if err := s.dropFromGroup(ctx, tx, g, userID, out); err != nil {
			return err
		}
	case err != nil && !errors.Is(err, ErrNotFound):
		return err
	}
	return tx.RemoveCompanyMember(ctx, c.ID, userID)
}
