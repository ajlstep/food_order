// Package api says which subject is answered by which function. It is the
// only place where the subjects of contracts/users meet the handlers.
package api

import (
	"context"
	"log/slog"
	"time"

	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
	"food-order/users/internal/auth"
	"food-order/users/internal/profile"
)

// Register connects every subject of the service to its handler.
func Register(srv *rpc.Server, log *slog.Logger, authHandler *auth.Handler, profileHandler *profile.Handler) error {
	routes := []struct {
		subject string
		handler rpc.Handler
	}{
		{users.SubjectPing, ping},
		{users.SubjectAuthResolve, authHandler.Resolve},
		{users.SubjectAuthEmailRequest, authHandler.EmailRequest},
		{users.SubjectAuthEmailConfirm, authHandler.EmailConfirm},
		{users.SubjectAuthIdentityStatus, authHandler.IdentityStatus},
		{users.SubjectAuthPhoneSet, profileHandler.PhoneSet},

		{users.SubjectProfileGet, profileHandler.Get},
		{users.SubjectProfileUpdate, profileHandler.Update},
		{users.SubjectProfileSettingsGet, profileHandler.SettingsGet},
		{users.SubjectProfileSettingsSet, profileHandler.SettingsSet},
		{users.SubjectProfileAllergensGet, profileHandler.AllergensGet},
		{users.SubjectProfileAllergensSet, profileHandler.AllergensSet},
		{users.SubjectProfilePrefsGet, profileHandler.PrefsGet},
		{users.SubjectProfilePrefsSet, profileHandler.PrefsSet},
		{users.SubjectProfileFoodGet, profileHandler.FoodGet},
		{users.SubjectProfileLocationsList, profileHandler.LocationsList},
		{users.SubjectProfileLocationsSave, profileHandler.LocationsSave},
		{users.SubjectProfileLocationsDel, profileHandler.LocationsDelete},
		{users.SubjectProfileLocationsMain, profileHandler.LocationsDefault},
		{users.SubjectProfileChannelsGet, profileHandler.ChannelsGet},
	}
	for _, r := range routes {
		if err := srv.Handle(r.subject, traced(log, r.subject, r.handler)); err != nil {
			return err
		}
	}
	return nil
}

func ping(context.Context, rpc.Request) (any, error) {
	return map[string]string{"service": "users", "status": "ok"}, nil
}

// traced writes a debug record for every request (what, how long, how it
// ended). It never writes the data: it holds emails and codes.
func traced(log *slog.Logger, subject string, next rpc.Handler) rpc.Handler {
	return func(ctx context.Context, req rpc.Request) (any, error) {
		start := time.Now()
		data, err := next(ctx, req)
		outcome := "ok"
		if err != nil {
			outcome = err.Error()
			if re, ok := err.(*rpc.Error); ok {
				outcome = re.Code
			}
		}
		log.DebugContext(ctx, "rpc handled", "subject", subject, "outcome", outcome, "ms", time.Since(start).Milliseconds())
		return data, err
	}
}
