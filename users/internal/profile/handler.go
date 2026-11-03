package profile

import (
	"context"
	"time"

	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
	"food-order/users/internal/refusal"
)

// Handler is the doorway of the profile zone: it reads the message, calls
// the rules, and shapes the answer. It holds no rules itself.
//
// The person is always the one in the envelope (rpc.Request.UserID), put
// there by the gateway after it knew who wrote. A user_id inside the data
// would let a caller ask for somebody else's profile, so it is not read.
type Handler struct{ svc *Service }

// NewHandler makes the handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// who returns the person of the request, or a bad_request if there is none.
func who(req rpc.Request) (string, error) {
	if req.UserID == "" {
		return "", rpc.NewError(rpc.CodeBadRequest, "the request has no user_id")
	}
	return req.UserID, nil
}

// Get answers rpc.users.profile.get.
func (h *Handler) Get(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	a, err := h.svc.Profile(ctx, id)
	return profileView(a), refusal.ToRPC(err)
}

// Update answers rpc.users.profile.update.
func (h *Handler) Update(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.ProfileUpdateRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	a, err := h.svc.UpdateProfile(ctx, id, ProfileChange{DisplayName: in.DisplayName, Language: in.Language})
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return profileView(a), nil
}

// PhoneSet answers rpc.users.auth.phone.set. It is in the auth zone because
// the bot sets the phone while the person registers; the person is named in
// the data, as in the other calls of that zone.
func (h *Handler) PhoneSet(ctx context.Context, req rpc.Request) (any, error) {
	var in users.PhoneSetRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	return nil, refusal.ToRPC(h.svc.SetPhone(ctx, in.UserID, in.Phone))
}

// Due answers rpc.users.profile.due. It is for core, and is about everybody;
// the rights in nats.conf say who may ask, and the gateway has no route to it.
func (h *Handler) Due(ctx context.Context, req rpc.Request) (any, error) {
	var in users.DueRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	if in.WindowMinutes <= 0 {
		return nil, rpc.NewError(rpc.CodeBadRequest, "window_minutes must be positive")
	}
	items, err := h.svc.Due(ctx, time.UnixMilli(in.AtMs), time.Duration(in.WindowMinutes)*time.Minute)
	if err != nil {
		return nil, err
	}
	out := users.DueResponse{Items: make([]users.DueItem, 0, len(items))}
	for _, it := range items {
		out.Items = append(out.Items, users.DueItem{UserID: it.UserID, ForDate: it.ForDate})
	}
	return out, nil
}

// SettingsGet answers rpc.users.profile.settings.get.
func (h *Handler) SettingsGet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	m, err := h.svc.Settings(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.SettingsResponse{Settings: m}, nil
}

// SettingsSet answers rpc.users.profile.settings.set.
func (h *Handler) SettingsSet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.SettingsSetRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	m, err := h.svc.SetSettings(ctx, id, in.Values)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.SettingsResponse{Settings: m}, nil
}

// AllergensGet answers rpc.users.profile.allergens.get.
func (h *Handler) AllergensGet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	l, err := h.svc.Allergens(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.AllergensResponse{Allergens: l}, nil
}

// AllergensSet answers rpc.users.profile.allergens.set.
func (h *Handler) AllergensSet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.AllergensSetRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	l, err := h.svc.SetAllergens(ctx, id, in.Allergens)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.AllergensResponse{Allergens: l}, nil
}

// PrefsGet answers rpc.users.profile.prefs.get.
func (h *Handler) PrefsGet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	items, err := h.svc.Prefs(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.PrefsResponse{Items: prefsView(items)}, nil
}

// PrefsSet answers rpc.users.profile.prefs.set.
func (h *Handler) PrefsSet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.PrefsSetRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	items := make([]Pref, len(in.Items))
	for i, p := range in.Items {
		items[i] = Pref{Kind: p.Kind, Value: p.Value, Hard: p.Hard}
	}
	saved, err := h.svc.SetPrefs(ctx, id, items)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.PrefsResponse{Items: prefsView(saved)}, nil
}

// FoodGet answers rpc.users.profile.food.get.
func (h *Handler) FoodGet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	allergens, prefs, err := h.svc.Food(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.FoodResponse{Allergens: allergens, Prefs: prefsView(prefs)}, nil
}

// LocationsList answers rpc.users.profile.locations.list.
func (h *Handler) LocationsList(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.Locations(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.LocationsResponse{Items: locationsView(list)}, nil
}

// LocationsSave answers rpc.users.profile.locations.save.
func (h *Handler) LocationsSave(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.Location
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	saved, err := h.svc.SaveLocation(ctx, id, Location{
		ID: in.ID, Label: in.Label, Address: in.Address, Lat: in.Lat, Lon: in.Lon, Notes: in.Notes, IsDefault: in.IsDefault,
	})
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return locationView(saved), nil
}

// LocationsDelete answers rpc.users.profile.locations.delete.
func (h *Handler) LocationsDelete(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.LocationIDRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	return nil, refusal.ToRPC(h.svc.DeleteLocation(ctx, id, in.ID))
}

// LocationsDefault answers rpc.users.profile.locations.default.
func (h *Handler) LocationsDefault(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.LocationIDRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	return nil, refusal.ToRPC(h.svc.SetDefaultLocation(ctx, id, in.ID))
}

// ChannelsGet answers rpc.users.profile.channels.get.
func (h *Handler) ChannelsGet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	list, latest, err := h.svc.Channels(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	out := users.ChannelsResponse{Channels: make([]users.Channel, len(list)), Latest: latest}
	for i, c := range list {
		out.Channels[i] = users.Channel{Channel: c.Channel, ChatRef: c.ChatRef, Status: c.Status, LastSeenMs: c.LastSeenAt.UnixMilli()}
	}
	return out, nil
}

// ---- the shapes of the answers

func profileView(a Account) users.ProfileResponse {
	return users.ProfileResponse{UserID: a.ID, DisplayName: a.DisplayName, Email: a.Email, Phone: a.Phone, Language: a.Language, Status: a.Status}
}

func prefsView(items []Pref) []users.Pref {
	out := make([]users.Pref, len(items)) // never nil: the caller gets [] and not null
	for i, p := range items {
		out[i] = users.Pref{Kind: p.Kind, Value: p.Value, Hard: p.Hard}
	}
	return out
}

func locationView(l Location) users.Location {
	return users.Location{ID: l.ID, Label: l.Label, Address: l.Address, Lat: l.Lat, Lon: l.Lon, Notes: l.Notes, IsDefault: l.IsDefault}
}

func locationsView(list []Location) []users.Location {
	out := make([]users.Location, len(list))
	for i, l := range list {
		out[i] = locationView(l)
	}
	return out
}
