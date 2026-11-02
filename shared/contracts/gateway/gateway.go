// Package gateway describes the HTTP side of the api-gateway: the paths, the
// headers a caller sends, the shape of every answer and the error codes the
// gateway adds itself. The gateway and its callers (the bot) import the same
// file, so they cannot disagree.
//
// The data of each route is the data of the NATS request behind it, see
// contracts/users. The full description, in words, is in the vault:
// services/Gateway.md and etape/E4-Gateway.md.
package gateway

import (
	"encoding/json"

	"food-order/shared/rpc"
)

// Headers.
const (
	// HeaderAuthorization carries the key of the calling service: "Bearer <key>".
	HeaderAuthorization = "Authorization"
	// HeaderRequestID: the caller may send its own id (8-64 characters of
	// A-Z a-z 0-9 . _ -) to follow a request through its logs; otherwise the
	// gateway makes one. The answer always carries it back.
	HeaderRequestID = "X-Request-ID"
	// HeaderChannel and HeaderExternalID say who the person is, as the
	// channel knows them: "telegram" and the Telegram user id.
	HeaderChannel    = "X-Channel"
	HeaderExternalID = "X-External-ID"
)

// Paths. The method is written in front, the way net/http patterns are.
const (
	// Bot only: the gateway does not look the person up before these.
	Identify      = "POST /v1/auth/identify"       // users.ResolveRequest (chat_ref, nickname, display_name, language)
	ChannelStatus = "POST /v1/auth/channel-status" // {"status": "stopped"}, see users.IdentityStatusRequest

	// Any account that is not blocked, including one waiting for its email.
	EmailRequest = "POST /v1/auth/email"         // {"email": "..."}
	EmailConfirm = "POST /v1/auth/email/confirm" // {"code": "..."}
	PhoneSet     = "POST /v1/auth/phone"         // {"phone": "..."}

	// Active accounts only (email confirmed).
	ProfileGet       = "GET /v1/me"
	ProfileUpdate    = "PATCH /v1/me"
	SettingsGet      = "GET /v1/me/settings"
	SettingsSet      = "PATCH /v1/me/settings"
	AllergensGet     = "GET /v1/me/allergens"
	AllergensSet     = "PUT /v1/me/allergens"
	PrefsGet         = "GET /v1/me/prefs"
	PrefsSet         = "PUT /v1/me/prefs"
	LocationsList    = "GET /v1/me/locations"
	LocationsSave    = "POST /v1/me/locations"
	LocationsDelete  = "DELETE /v1/me/locations/{id}"
	LocationsDefault = "POST /v1/me/locations/{id}/default"
	ChannelsGet      = "GET /v1/me/channels"

	Health = "GET /healthz" // no key needed
)

// Error codes the gateway adds to the ones of the services.
const (
	ErrUnauthorized   = "unauthorized"    // no key, or a wrong one (HTTP 401)
	ErrNoIdentity     = "no_identity"     // X-Channel or X-External-ID missing (HTTP 400)
	ErrChannelRefused = "channel_refused" // this caller may not speak for that channel (HTTP 403)
	ErrNotFoundRoute  = "no_route"        // no such path (HTTP 404)
	ErrBodyTooLarge   = "body_too_large"  // HTTP 413
)

// Response is every answer of the gateway: either Data or Error.
type Response struct {
	OK        bool            `json:"ok"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     *rpc.Error      `json:"error,omitempty"`
	RequestID string          `json:"request_id"`
}

// ChannelStatusRequest is the body of ChannelStatus. The channel and the id
// come from the headers.
type ChannelStatusRequest struct {
	Status string `json:"status"` // active | stopped | blocked
}
