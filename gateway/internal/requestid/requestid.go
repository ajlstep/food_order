// Package requestid gives every HTTP request an id, the first step of the
// gateway. The id goes into the logs of the gateway, into the NATS envelope
// (so the services log it too) and back to the caller in X-Request-ID. With
// it one request can be followed through bot, gateway and users.
//
// It does not check who calls; that is package access.
package requestid

import (
	"context"
	"crypto/rand"
	"net/http"
	"regexp"

	"food-order/gateway/internal/applog"
	"food-order/shared/contracts/gateway"
)

type ctxKey struct{}

// The caller's own id is taken only when it looks harmless: it ends up in
// logs and headers, so no spaces, no line breaks, nothing long.
var valid = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// Middleware puts the id on the request context and on the answer.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(gateway.HeaderRequestID)
		if !valid.MatchString(id) {
			id = rand.Text()
		}
		w.Header().Set(gateway.HeaderRequestID, id)

		ctx := context.WithValue(r.Context(), ctxKey{}, id)
		ctx = applog.With(ctx, "request_id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// From returns the id of the request ("" outside Middleware).
func From(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}
