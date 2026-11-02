// Package rpc is how food-order services ask each other for something over
// NATS: one request, one reply (plain NATS, not JetStream).
//
// Subjects look like rpc.<service>.<zone>.<action>, for example
// rpc.users.auth.resolve. They must never fall under a JetStream stream
// subject (food.>, logs.>, notify.>): a stream confirms every message it
// receives, and the caller could take that confirmation for the answer.
//
// This file holds the shapes that go over the wire; client.go and server.go
// hold the two sides.
package rpc

import (
	"encoding/json"
	"fmt"
)

// Version is the version of the envelope. A request with another version is refused.
const Version = 1

// Request is what the caller sends.
type Request struct {
	V         int             `json:"v"`
	RequestID string          `json:"request_id"`
	UserID    string          `json:"user_id,omitempty"` // who the call is for, when the gateway already knows
	Data      json.RawMessage `json:"data,omitempty"`
}

// Response is what the service answers: either Data or Error.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error *Error          `json:"error,omitempty"`
}

// Error codes that are not specific to one service.
const (
	CodeBadRequest  = "bad_request" // the message is not valid (HTTP 400)
	CodeInternal    = "internal"    // something broke on our side (HTTP 500)
	CodeUnavailable = "unavailable" // nobody listens on the subject (HTTP 503)
	CodeTimeout     = "timeout"     // nobody answered in time (HTTP 504)
	CodeNotFound    = "not_found"   // HTTP 404
)

// Error is an error that crosses the wire. Code is stable and meant for
// programs; Message is for humans (logs); Details carries numbers the caller
// may need, for example retry_after or attempts_left.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// Decode reads the data of a request into v. A request without data, or with
// data of the wrong shape, is refused with CodeBadRequest.
func Decode(req Request, v any) error {
	if len(req.Data) == 0 || string(req.Data) == "null" {
		return NewError(CodeBadRequest, "the request has no data")
	}
	if err := json.Unmarshal(req.Data, v); err != nil {
		return NewError(CodeBadRequest, "the data of the request is not valid: "+err.Error())
	}
	return nil
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// NewError builds an *Error without details.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}
