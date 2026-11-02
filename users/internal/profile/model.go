package profile

import (
	"errors"
	"time"
)

// ErrNotFound and ErrConflict are what the store says; the rules decide what they mean.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

// Account states (the same words as in contracts/users).
const (
	StatusPending = "pending"
	StatusActive  = "active"
)

// Account is the person's own data.
type Account struct {
	ID          string
	DisplayName string
	Email       string
	Phone       string
	Language    string
	Status      string
}

// Pref is a food preference. Kind is one of like, dislike, diet, target.
type Pref struct {
	Kind  string
	Value string
	Hard  bool
}

// Location is a saved address.
type Location struct {
	ID        int64
	Label     string
	Address   string
	Lat, Lon  *float64
	Notes     string
	IsDefault bool
}

// Channel is one channel account of the person.
type Channel struct {
	Channel    string
	ChatRef    string
	Status     string
	LastSeenAt time.Time
}
