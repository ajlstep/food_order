package org

import (
	"errors"
	"time"
)

// ErrNotFound and ErrConflict are what the store says; the rules decide what they mean.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

// statusActive is the account state that may use this zone.
const statusActive = "active"

// Invitation states.
const (
	inviteOpen     = "open"
	inviteAccepted = "accepted"
	inviteDeclined = "declined"
)

// Company is a company. MaxFee is in bani: nil = no limit, 0 = only free delivery.
type Company struct {
	ID      string
	Name    string
	OwnerID string
	MaxFee  *int64
}

// Group is a common address. CompanyID is "" for a free group.
type Group struct {
	ID          string
	Name        string
	OwnerID     string
	CompanyID   string
	CompanyName string
	Address     string
	Lat, Lon    *float64
	Notes       string
}

// Member is one person of a company or group.
type Member struct {
	UserID      string
	DisplayName string
	Nickname    string
	Joined      time.Time
}

// Invite is an invitation. Name is the name of the company or group.
type Invite struct {
	ID       string
	Kind     string
	TargetID string
	Name     string
	FromUser string
	ToUser   string
	Status   string
	Expires  time.Time
}
