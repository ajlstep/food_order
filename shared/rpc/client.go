package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go"
)

// Client sends requests and waits for the answer.
type Client struct {
	nc *nats.Conn
}

// NewClient wraps a NATS connection. How long a call may take is decided by
// the ctx the caller passes (the gateway uses 5 seconds).
func NewClient(nc *nats.Conn) *Client { return &Client{nc: nc} }

// Call sends in as the data of a request and decodes the reply into out
// (out may be nil when the reply has no data).
//
// A failed call returns an *Error: the one the service sent, or
// CodeUnavailable (no service listens) or CodeTimeout (no answer in time).
func (c *Client) Call(ctx context.Context, subject, requestID string, in, out any) error {
	return c.CallFor(ctx, subject, requestID, "", in, out)
}

// CallFor is Call for one person: userID goes in the envelope, where the
// services that work on a person's data read it (the profile zone of users).
func (c *Client) CallFor(ctx context.Context, subject, requestID, userID string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("rpc %s: encode request: %w", subject, err)
	}
	raw, err := json.Marshal(Request{V: Version, RequestID: requestID, UserID: userID, Data: data})
	if err != nil {
		return fmt.Errorf("rpc %s: encode envelope: %w", subject, err)
	}

	msg, err := c.nc.RequestWithContext(ctx, subject, raw)
	switch {
	case errors.Is(err, nats.ErrNoResponders):
		return NewError(CodeUnavailable, "no service listens on "+subject)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout):
		return NewError(CodeTimeout, "no answer from "+subject)
	case err != nil:
		return fmt.Errorf("rpc %s: %w", subject, err)
	}

	var resp Response
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return fmt.Errorf("rpc %s: decode reply: %w", subject, err)
	}
	if !resp.OK {
		if resp.Error == nil {
			return NewError(CodeInternal, "the service answered ok=false without an error")
		}
		return resp.Error
	}
	if out != nil && len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, out); err != nil {
			return fmt.Errorf("rpc %s: decode data: %w", subject, err)
		}
	}
	return nil
}
