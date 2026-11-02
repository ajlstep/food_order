// Package users describes the messages of the users service: subjects, the
// shape of requests and replies, and the error codes. The caller (gateway)
// and the service import the same file, so they cannot disagree.
//
// The full description, in words, is in the vault: services/Users.md.
package users

// Subjects. All of them are rpc.users.<zone>.<action>.
const (
	SubjectPing = "rpc.users.ping" // is the service alive

	SubjectAuthResolve        = "rpc.users.auth.resolve"
	SubjectAuthEmailRequest   = "rpc.users.auth.email.request"
	SubjectAuthEmailConfirm   = "rpc.users.auth.email.confirm"
	SubjectAuthIdentityStatus = "rpc.users.auth.identity.status"
	SubjectAuthPhoneSet       = "rpc.users.auth.phone.set"

	// SubjectAuthAll is what the service listens to for the auth zone.
	SubjectAll = "rpc.users.>"
)

// Account states.
const (
	StatusPending = "pending" // started, email not confirmed yet
	StatusActive  = "active"
	StatusBlocked = "blocked"
	StatusDeleted = "deleted"
)

// States of a channel account (identity).
const (
	IdentityActive  = "active"
	IdentityStopped = "stopped" // the person sent /stop
	IdentityBlocked = "blocked" // the person blocked the bot
)

// Error codes of the auth zone. Details, when there are any, are in brackets.
const (
	ErrInvalidInput     = "invalid_input"     // a field is missing or malformed
	ErrInvalidEmail     = "invalid_email"     // not an email address
	ErrCooldown         = "cooldown"          // asked again too soon (retry_after, seconds)
	ErrTooManyCodes     = "too_many_codes"    // too many codes in an hour (retry_after, seconds)
	ErrWrongCode        = "wrong_code"        // (attempts_left)
	ErrExpiredCode      = "expired"           // the code is older than its lifetime
	ErrTooManyAttempts  = "too_many_attempts" // the code was cancelled after wrong attempts
	ErrNoCode           = "no_code"           // no code was asked for
	ErrAccountNotFound  = "account_not_found" // not_found for users and identities
	ErrAccountBlocked   = "account_blocked"   // the account (or the one the email belongs to) is blocked
	ErrAlreadyConfirmed = "already_confirmed" // this account has a confirmed email already
	ErrPhoneTaken       = "phone_taken"       // another account has this phone
	ErrInvalidPhone     = "invalid_phone"
)

// ---------------------------------------------------------------- auth.resolve

// ResolveRequest says who wrote, as the channel knows them. The service
// creates a pending account for a person it has not seen before.
type ResolveRequest struct {
	Channel     string `json:"channel"`            // telegram
	ExternalID  string `json:"external_id"`        // the person's id in the channel
	ChatRef     string `json:"chat_ref,omitempty"` // where to send messages (Telegram: chat id)
	Nickname    string `json:"nickname,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Language    string `json:"language,omitempty"`
}

// ResolveResponse says who the person is.
type ResolveResponse struct {
	UserID      string `json:"user_id"`
	Status      string `json:"status"`
	IsNew       bool   `json:"is_new"`
	Language    string `json:"language"`
	DisplayName string `json:"display_name"`
}

// ---------------------------------------------------------------- auth.email.request

// EmailRequestRequest asks for a confirmation code to be sent to Email.
type EmailRequestRequest struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

// EmailRequestResponse only says the code was accepted for sending. It
// does not tell whether the address already belongs to an account.
type EmailRequestResponse struct {
	Sent           bool `json:"sent"`
	ValidForSecond int  `json:"valid_for_seconds"` // how long the code works
}

// ---------------------------------------------------------------- auth.email.confirm

// Results of a confirmation.
const (
	ConfirmConfirmed = "confirmed" // the email now belongs to this account
	ConfirmAttached  = "attached"  // the channel moved to the account that already owned the email
)

// EmailConfirmRequest carries the code the person typed.
type EmailConfirmRequest struct {
	UserID string `json:"user_id"`
	Code   string `json:"code"`
}

// EmailConfirmResponse. After "attached" the UserID is another one than the
// request had: from now on the caller must use this one.
type EmailConfirmResponse struct {
	UserID string `json:"user_id"`
	Result string `json:"result"`
}

// ---------------------------------------------------------------- auth.identity.status

// IdentityStatusRequest changes the state of a channel account: /stop, the
// bot was blocked, or the person came back.
type IdentityStatusRequest struct {
	Channel    string `json:"channel"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
}

// ---------------------------------------------------------------- auth.phone.set

// PhoneSetRequest saves the phone of an account. The caller (the bot) has
// checked that the phone is the person's own: it came from the "send my
// number" button of the messenger.
type PhoneSetRequest struct {
	UserID string `json:"user_id"`
	Phone  string `json:"phone"`
}
