// Package forward turns one HTTP request into one NATS request, and the
// answer back into HTTP: the last step of the gateway, after access.
//
// It holds no rules of its own. What goes into the NATS request is decided
// per route (a DataFunc, see package api); by default the body goes as it is.
package forward

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"food-order/gateway/internal/access"
	"food-order/gateway/internal/reply"
	"food-order/gateway/internal/requestid"
	"food-order/shared/contracts/gateway"
	"food-order/shared/rpc"
)

// maxBody is the largest body the gateway reads. The biggest real one, a
// list of 200 preferences, is far below.
const maxBody = 64 << 10

// Caller sends a request to a service (rpc.Client does it).
type Caller interface {
	CallFor(ctx context.Context, subject, requestID, userID string, in, out any) error
}

// DataFunc builds the data of the NATS request from the HTTP request and its
// body. It returns an *rpc.Error (bad_request) when the body is wrong.
type DataFunc func(r *http.Request, body []byte) (any, error)

// Forwarder sends HTTP requests on to the services.
type Forwarder struct {
	caller  Caller
	timeout time.Duration
	log     *slog.Logger
}

// New makes a Forwarder; timeout is how long a service may take to answer.
func New(caller Caller, timeout time.Duration, log *slog.Logger) *Forwarder {
	return &Forwarder{caller: caller, timeout: timeout, log: log}
}

// To answers a route by asking subject. data may be nil: the body is sent
// as it is.
func (f *Forwarder) To(subject string, data DataFunc) http.Handler {
	if data == nil {
		data = asIs
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		err := f.forward(w, r, subject, data)
		f.logOutcome(r.Context(), subject, start, err)
		if err != nil {
			reply.Fail(w, r, err)
		}
	})
}

// forward does the work of To; on success it has already answered.
func (f *Forwarder) forward(w http.ResponseWriter, r *http.Request, subject string, data DataFunc) error {
	body, err := readBody(w, r)
	if err != nil {
		return err
	}
	in, err := data(r, body)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(r.Context(), f.timeout)
	defer cancel()
	// The person always goes in the envelope; the services that work on a
	// person's data take it only from there.
	userID := access.PersonFrom(ctx).UserID
	var out json.RawMessage
	if err := f.caller.CallFor(ctx, subject, requestid.From(ctx), userID, in, &out); err != nil {
		return err
	}
	reply.Data(w, r, out)
	return nil
}

func (f *Forwarder) logOutcome(ctx context.Context, subject string, start time.Time, err error) {
	ms := time.Since(start).Milliseconds()
	var re *rpc.Error
	switch {
	case err == nil:
		f.log.DebugContext(ctx, "forwarded", "subject", subject, "outcome", "ok", "ms", ms)
	case errors.As(err, &re) && reply.Status(re.Code) < 500:
		f.log.DebugContext(ctx, "forwarded", "subject", subject, "outcome", re.Code, "ms", ms)
	default:
		f.log.ErrorContext(ctx, "forward failed", "subject", subject, "err", err, "ms", ms)
	}
}

// readBody reads at most maxBody bytes.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, rpc.NewError(gateway.ErrBodyTooLarge, "the body is larger than 64 KiB")
	}
	if err != nil {
		return nil, rpc.NewError(rpc.CodeBadRequest, "cannot read the body")
	}
	return body, nil
}

// asIs sends the body unchanged; it only has to be JSON.
func asIs(_ *http.Request, body []byte) (any, error) {
	if len(body) == 0 {
		return nil, nil
	}
	if !json.Valid(body) {
		return nil, rpc.NewError(rpc.CodeBadRequest, "the body is not valid JSON")
	}
	return json.RawMessage(body), nil
}

// Decode reads a JSON body into v, for the DataFuncs that need to add
// something to it. An empty or malformed body is a bad_request.
func Decode(body []byte, v any) error {
	if len(body) == 0 {
		return rpc.NewError(rpc.CodeBadRequest, "the request has no body")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return rpc.NewError(rpc.CodeBadRequest, "the body is not valid: "+err.Error())
	}
	return nil
}
