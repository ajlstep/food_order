// Package reply writes every answer of the gateway in the same shape
// (gateway.Response) and chooses the HTTP status from the error code.
//
// It does not log; whoever calls it decides what is worth logging.
package reply

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"food-order/gateway/internal/requestid"
	"food-order/shared/contracts/gateway"
	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
)

// statusOf maps the stable error codes to HTTP. A code that is not here is
// a "no" from a service that the gateway does not know yet: 422.
var statusOf = map[string]int{
	rpc.CodeBadRequest:      http.StatusBadRequest,
	users.ErrInvalidInput:   http.StatusBadRequest,
	users.ErrInvalidEmail:   http.StatusBadRequest,
	users.ErrInvalidPhone:   http.StatusBadRequest,
	users.ErrTooMany:        http.StatusBadRequest, // a list longer than allowed
	gateway.ErrNoIdentity:   http.StatusBadRequest,
	gateway.ErrUnauthorized: http.StatusUnauthorized,

	gateway.ErrChannelRefused:  http.StatusForbidden,
	users.ErrEmailNotConfirmed: http.StatusForbidden,
	users.ErrAccountBlocked:    http.StatusForbidden,

	rpc.CodeNotFound:         http.StatusNotFound,
	gateway.ErrNotFoundRoute: http.StatusNotFound,

	users.ErrAlreadyConfirmed: http.StatusConflict,
	users.ErrPhoneTaken:       http.StatusConflict,

	gateway.ErrBodyTooLarge: http.StatusRequestEntityTooLarge,

	users.ErrCooldown:        http.StatusTooManyRequests,
	users.ErrTooManyCodes:    http.StatusTooManyRequests,
	users.ErrTooManyAttempts: http.StatusTooManyRequests,

	rpc.CodeInternal:    http.StatusInternalServerError,
	rpc.CodeUnavailable: http.StatusServiceUnavailable,
	rpc.CodeTimeout:     http.StatusGatewayTimeout,
}

// Status is the HTTP status for an error code.
func Status(code string) int {
	if s, ok := statusOf[code]; ok {
		return s
	}
	return http.StatusUnprocessableEntity
}

// Data writes a successful answer. data may be empty.
func Data(w http.ResponseWriter, r *http.Request, data json.RawMessage) {
	write(w, http.StatusOK, gateway.Response{OK: true, Data: data, RequestID: requestid.From(r.Context())})
}

// Fail writes an error. An *rpc.Error goes out as it is (the services choose
// what a person may see); any other error is ours and goes out only as
// "internal", without details.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var re *rpc.Error
	if !errors.As(err, &re) {
		re = rpc.NewError(rpc.CodeInternal, "internal error")
	}
	status := Status(re.Code)
	if status >= 500 {
		// subjects and service names stay inside
		re = &rpc.Error{Code: re.Code, Message: http.StatusText(status)}
	}
	if s, ok := retryAfter(re); ok {
		w.Header().Set("Retry-After", s)
	}
	write(w, status, gateway.Response{Error: re, RequestID: requestid.From(r.Context())})
}

// retryAfter reads details.retry_after (seconds), as users sends it with
// cooldown and too_many_codes.
func retryAfter(e *rpc.Error) (string, bool) {
	v, ok := e.Details["retry_after"]
	if !ok {
		return "", false
	}
	switch n := v.(type) {
	case float64: // a number that came through JSON
		return fmt.Sprintf("%d", int64(n+0.999)), true
	case int, int64:
		return fmt.Sprint(n), true
	}
	return "", false
}

func write(w http.ResponseWriter, status int, resp gateway.Response) {
	raw, err := json.Marshal(resp)
	if err != nil {
		// cannot happen with the types above; answer something valid anyway
		raw = []byte(`{"ok":false,"error":{"code":"internal"}}`)
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
