package auth

import (
	"errors"
	"time"
)

// Account states (the same words as in contracts/users).
const (
	StatusPending = "pending"
	StatusActive  = "active"
	StatusBlocked = "blocked"
	StatusDeleted = "deleted"
)

// Channel-account states.
const (
	IdentityActive  = "active"
	IdentityStopped = "stopped"
	IdentityBlocked = "blocked"
)

// Errors of the store. The rules decide what they mean for the person.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

// User is a person's account.
type User struct {
	ID          string
	DisplayName string
	Email       string // empty until confirmed
	Language    string
	Status      string
	CreatedAt   time.Time
}

// Identity is one channel account of a person, for example their Telegram.
type Identity struct {
	ID          int64
	UserID      string
	Channel     string
	ExternalID  string
	ChatRef     string
	Nickname    string
	DisplayName string
	Language    string
	Status      string
	LastSeenAt  time.Time
	CreatedAt   time.Time
}

// CodeRecord is the confirmation code that is valid now for an account.
type CodeRecord struct {
	UserID    string
	Email     string
	CodeHash  string
	Attempts  int
	CreatedAt time.Time
	ExpiresAt time.Time
}
