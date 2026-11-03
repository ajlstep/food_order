package profile

import (
	"context"
	"time"
)

// AccountStore: the person's own data and channels.
type AccountStore interface {
	Account(ctx context.Context, userID string) (Account, error)
	SaveAccount(ctx context.Context, a Account, at time.Time) error // name and language
	// SetPhone returns ErrConflict if another account has this phone.
	SetPhone(ctx context.Context, userID, phone string, at time.Time) error
	// Channels lists the channel accounts, the most recently seen first.
	Channels(ctx context.Context, userID string) ([]Channel, error)
}

// SettingsStore: only what the person saved; defaults are added by the rules.
type SettingsStore interface {
	Settings(ctx context.Context, userID string) (map[string]string, error)
	SaveSettings(ctx context.Context, userID string, values map[string]string) error
}

// FoodStore: allergens and preferences are always replaced as a whole.
type FoodStore interface {
	Allergens(ctx context.Context, userID string) ([]string, error)
	ReplaceAllergens(ctx context.Context, userID string, labels []string) error
	Prefs(ctx context.Context, userID string) ([]Pref, error)
	ReplacePrefs(ctx context.Context, userID string, items []Pref) error
}

// LocationStore: saved addresses.
type LocationStore interface {
	Locations(ctx context.Context, userID string) ([]Location, error) // oldest first
	// SaveLocation inserts a location with ID 0, or updates the one with that
	// ID (ErrNotFound if it is not this person's). It returns the saved one.
	SaveLocation(ctx context.Context, userID string, l Location, at time.Time) (Location, error)
	// DeleteLocation says whether the deleted one was the default.
	DeleteLocation(ctx context.Context, userID string, id int64) (wasDefault bool, err error)
	// SetDefaultLocation makes this one the default and the others not.
	SetDefaultLocation(ctx context.Context, userID string, id int64) error
}

// Tx is the store inside a transaction: all or nothing.
type Tx interface {
	AccountStore
	SettingsStore
	FoodStore
	LocationStore
}

// Store is the whole store.
type Store interface {
	Tx
	// DueCandidates lists the active people with an active channel, with what
	// they saved in the settings.
	DueCandidates(ctx context.Context) ([]DueCandidate, error)
	InTx(ctx context.Context, fn func(Tx) error) error
}
