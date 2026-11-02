// Package refusal is the way the rules of users say "no" to a person: a wrong
// code, too soon, a value that is not allowed. It is not a malfunction, so it
// has a stable code the caller can act on, and it crosses the wire as it is.
// Real malfunctions (the database is down) are ordinary Go errors; the rpc
// server logs them and answers only "internal".
package refusal

import (
	"errors"
	"fmt"
	"strings"

	"food-order/shared/rpc"
)

// Error is a refusal. Details carry numbers the caller may show, such as
// retry_after or attempts_left.
type Error struct {
	Code    string
	Details map[string]any
}

func (e *Error) Error() string {
	if len(e.Details) == 0 {
		return e.Code
	}
	parts := make([]string, 0, len(e.Details))
	for k, v := range e.Details {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return e.Code + " (" + strings.Join(parts, ", ") + ")"
}

// New builds a refusal; the details are key, value pairs.
func New(code string, details ...any) *Error {
	e := &Error{Code: code}
	if len(details) > 0 {
		e.Details = make(map[string]any, len(details)/2)
		for i := 0; i+1 < len(details); i += 2 {
			e.Details[fmt.Sprint(details[i])] = details[i+1]
		}
	}
	return e
}

// codeAccountNotFound is how the rules say "no such account"; on the wire it
// is the general not_found.
const codeAccountNotFound = "account_not_found"

// ToRPC turns a refusal into an error that crosses the wire. Any other error
// is returned as it is.
func ToRPC(err error) error {
	var r *Error
	if !errors.As(err, &r) {
		return err
	}
	code := r.Code
	if code == codeAccountNotFound {
		code = rpc.CodeNotFound
	}
	return &rpc.Error{Code: code, Details: r.Details}
}
