package org

import (
	"context"
	"time"
)

// PeopleStore finds accounts.
type PeopleStore interface {
	AccountStatus(ctx context.Context, userID string) (string, error)
	UserByNickname(ctx context.Context, nickname string) (string, error) // ignores upper and lower case
	UserByPhone(ctx context.Context, phone string) (string, error)
}

// CompanyStore: companies and their members. Closed companies are not found.
type CompanyStore interface {
	CompanyOf(ctx context.Context, userID string) (Company, error)
	CompanyByID(ctx context.Context, id string) (Company, error)
	CreateCompany(ctx context.Context, c Company, at time.Time) error
	SaveCompany(ctx context.Context, c Company) error // name and limit
	CloseCompany(ctx context.Context, id string, at time.Time) error
	// AddCompanyMember returns ErrConflict if the person is in a company.
	AddCompanyMember(ctx context.Context, companyID, userID string, at time.Time) error
	RemoveCompanyMember(ctx context.Context, companyID, userID string) error
	CompanyMembers(ctx context.Context, companyID string) ([]Member, error) // oldest first
}

// GroupStore: groups and their members. Deleted groups are not found.
type GroupStore interface {
	GroupOf(ctx context.Context, userID string) (Group, error)
	GroupByID(ctx context.Context, id string) (Group, error)
	CreateGroup(ctx context.Context, g Group, at time.Time) error
	SetGroupOwner(ctx context.Context, groupID, userID string) error
	CloseGroup(ctx context.Context, id string, at time.Time) error
	// AddGroupMember returns ErrConflict if the person is in a group.
	AddGroupMember(ctx context.Context, groupID, userID string, at time.Time) error
	RemoveGroupMember(ctx context.Context, groupID, userID string) error
	GroupMembers(ctx context.Context, groupID string) ([]Member, error) // oldest first
}

// InviteStore: invitations.
type InviteStore interface {
	// CreateInvite returns ErrConflict if the same invitation is open.
	CreateInvite(ctx context.Context, i Invite, at time.Time) error
	Invite(ctx context.Context, id string) (Invite, error)
	OpenInvites(ctx context.Context, userID string, now time.Time) ([]Invite, error)
	SetInviteStatus(ctx context.Context, id, status string) error
	// ExpireInvites closes what passed its date, so an old invitation does
	// not stop a new one.
	ExpireInvites(ctx context.Context, now time.Time) error
}

// Tx is the store inside a transaction: all or nothing.
type Tx interface {
	PeopleStore
	CompanyStore
	GroupStore
	InviteStore
}

// Store is the whole store.
type Store interface {
	Tx
	InTx(ctx context.Context, fn func(Tx) error) error
}
