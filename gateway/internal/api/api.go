// Package api says which HTTP path goes to which NATS subject, and how much
// each one needs to know about the person. It is the only place where the
// paths of contracts/gateway meet the subjects of the services.
//
// A new route is one row in routes(); the checks and the forwarding are the
// same for all of them.
package api

import (
	"context"
	"net/http"
	"strconv"

	"food-order/gateway/internal/access"
	"food-order/gateway/internal/forward"
	"food-order/gateway/internal/reply"
	"food-order/gateway/internal/requestid"
	"food-order/shared/contracts/gateway"
	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
)

// route is one row of the table.
type route struct {
	pattern string           // method and path, from contracts/gateway
	level   access.Level     // what the gate asks of the person
	subject string           // where the request goes
	data    forward.DataFunc // how the NATS data is built; nil = the body as it is
}

func routes() []route {
	return []route{
		// registration: the bot calls identify at every new message
		{gateway.Identify, access.LevelChannel, users.SubjectAuthResolve, identifyData},
		{gateway.ChannelStatus, access.LevelChannel, users.SubjectAuthIdentityStatus, channelStatusData},
		{gateway.EmailRequest, access.LevelAccount, users.SubjectAuthEmailRequest, emailRequestData},
		{gateway.EmailConfirm, access.LevelAccount, users.SubjectAuthEmailConfirm, emailConfirmData},
		{gateway.PhoneSet, access.LevelAccount, users.SubjectAuthPhoneSet, phoneSetData},

		// the profile: users takes the person from the envelope
		{gateway.ProfileGet, access.LevelActive, users.SubjectProfileGet, nil},
		{gateway.ProfileUpdate, access.LevelActive, users.SubjectProfileUpdate, nil},
		{gateway.SettingsGet, access.LevelActive, users.SubjectProfileSettingsGet, nil},
		{gateway.SettingsSet, access.LevelActive, users.SubjectProfileSettingsSet, nil},
		{gateway.AllergensGet, access.LevelActive, users.SubjectProfileAllergensGet, nil},
		{gateway.AllergensSet, access.LevelActive, users.SubjectProfileAllergensSet, nil},
		{gateway.PrefsGet, access.LevelActive, users.SubjectProfilePrefsGet, nil},
		{gateway.PrefsSet, access.LevelActive, users.SubjectProfilePrefsSet, nil},
		{gateway.LocationsList, access.LevelActive, users.SubjectProfileLocationsList, nil},
		{gateway.LocationsSave, access.LevelActive, users.SubjectProfileLocationsSave, nil},
		{gateway.LocationsDelete, access.LevelActive, users.SubjectProfileLocationsDel, locationIDData},
		{gateway.LocationsDefault, access.LevelActive, users.SubjectProfileLocationsMain, locationIDData},
		{gateway.ChannelsGet, access.LevelActive, users.SubjectProfileChannelsGet, nil},
	}
}

// Health says whether the gateway can reach NATS.
type Health func() bool

// Handler builds the whole HTTP side: every route behind its checks, plus
// /healthz and a JSON answer for unknown paths.
func Handler(acc *access.Access, fwd *forward.Forwarder, healthy Health) http.Handler {
	mux := http.NewServeMux()
	for _, rt := range routes() {
		mux.Handle(rt.pattern, acc.Wrap(rt.level, fwd.To(rt.subject, rt.data)))
	}
	mux.HandleFunc(gateway.Health, func(w http.ResponseWriter, r *http.Request) {
		if !healthy() {
			reply.Fail(w, r, rpc.NewError(rpc.CodeUnavailable, "no connection to NATS"))
			return
		}
		reply.Data(w, r, []byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reply.Fail(w, r, rpc.NewError(gateway.ErrNotFoundRoute, r.Method+" "+r.URL.Path))
	})
	return requestid.Middleware(mux)
}

// ---------------------------------------------------------------- data

// The routes of the registration name the person inside the data (that is
// how users reads them). The gateway always writes it from what it knows,
// over anything the caller wrote: nobody can act for another account.

func identifyData(r *http.Request, body []byte) (any, error) {
	var in users.ResolveRequest
	if len(body) > 0 { // chat_ref, nickname, ... are optional
		if err := forward.Decode(body, &in); err != nil {
			return nil, err
		}
	}
	id := access.IdentityFrom(r.Context())
	in.Channel, in.ExternalID = id.Channel, id.ExternalID
	return in, nil
}

func channelStatusData(r *http.Request, body []byte) (any, error) {
	var in gateway.ChannelStatusRequest
	if err := forward.Decode(body, &in); err != nil {
		return nil, err
	}
	id := access.IdentityFrom(r.Context())
	return users.IdentityStatusRequest{Channel: id.Channel, ExternalID: id.ExternalID, Status: in.Status}, nil
}

func emailRequestData(r *http.Request, body []byte) (any, error) {
	var in users.EmailRequestRequest
	if err := forward.Decode(body, &in); err != nil {
		return nil, err
	}
	in.UserID = userID(r.Context())
	return in, nil
}

func emailConfirmData(r *http.Request, body []byte) (any, error) {
	var in users.EmailConfirmRequest
	if err := forward.Decode(body, &in); err != nil {
		return nil, err
	}
	in.UserID = userID(r.Context())
	return in, nil
}

func phoneSetData(r *http.Request, body []byte) (any, error) {
	var in users.PhoneSetRequest
	if err := forward.Decode(body, &in); err != nil {
		return nil, err
	}
	in.UserID = userID(r.Context())
	return in, nil
}

// locationIDData takes the id from the path (/v1/me/locations/{id}).
func locationIDData(r *http.Request, _ []byte) (any, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return nil, rpc.NewError(rpc.CodeBadRequest, "the location id must be a positive number")
	}
	return users.LocationIDRequest{ID: id}, nil
}

func userID(ctx context.Context) string { return access.PersonFrom(ctx).UserID }
