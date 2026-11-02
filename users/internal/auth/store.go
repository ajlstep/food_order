package auth

import (
	"context"
	"time"
)

// AccountStore is what the rules need to know about accounts and channels.
type AccountStore interface {
	UserByID(ctx context.Context, id string) (User, error)
	UserByEmail(ctx context.Context, email string) (User, error)
	IdentityByKey(ctx context.Context, channel, externalID string) (Identity, error)

	CreateUser(ctx context.Context, u User) error
	CreateIdentity(ctx context.Context, i Identity) error
	UpdateIdentity(ctx context.Context, i Identity) error
	SetIdentityStatus(ctx context.Context, channel, externalID, status string) error

	// MoveIdentities gives all channels of one account to another.
	MoveIdentities(ctx context.Context, fromUser, toUser string) error
	DeleteUser(ctx context.Context, id string) error
	// ConfirmEmail sets the email, marks it confirmed and makes the account
	// active. ErrConflict if another account has this email.
	ConfirmEmail(ctx context.Context, userID, email string, at time.Time) error
}

// CodeStore is what the rules need to know about confirmation codes.
type CodeStore interface {
	CodeByUser(ctx context.Context, userID string) (CodeRecord, error)
	// SaveCode replaces the code of the account, if there is one.
	SaveCode(ctx context.Context, c CodeRecord) error
	// AddWrongAttempt counts a wrong attempt and returns how many there are now.
	AddWrongAttempt(ctx context.Context, userID string) (int, error)
	DeleteCode(ctx context.Context, userID string) error

	// LastSend is when a code was last sent for this account (ErrNotFound if never).
	LastSend(ctx context.Context, userID string) (time.Time, error)
	RecordSend(ctx context.Context, userID, email string, at time.Time) error
	// SendsSince counts the codes sent to this email since the given time and
	// returns when the oldest of them was sent.
	SendsSince(ctx context.Context, email string, since time.Time) (count int, oldest time.Time, err error)
	// ForgetSendsBefore deletes old rows of the send log.
	ForgetSendsBefore(ctx context.Context, before time.Time) error
}

// Tx is the store inside a transaction: all or nothing.
type Tx interface {
	AccountStore
	CodeStore
}

// Store is the whole store. InTx runs fn in a transaction: if fn returns an
// error everything it did is undone. A refusal for the person (an *Error)
// must therefore not be returned from fn when the work done before it has to
// stay, for example counting a wrong attempt.
type Store interface {
	Tx
	InTx(ctx context.Context, fn func(Tx) error) error
}
