package org

import (
	"context"

	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
	"food-order/users/internal/refusal"
)

// Handler is the doorway of the org zone: it reads the message, calls the
// rules, and shapes the answer. As in the profile zone, the person is the one
// in the envelope, never one taken from the data.
type Handler struct{ svc *Service }

// NewHandler makes the handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func who(req rpc.Request) (string, error) {
	if req.UserID == "" {
		return "", rpc.NewError(rpc.CodeBadRequest, "the request has no user_id")
	}
	return req.UserID, nil
}

// withBody answers a request that has data and no answer but "done".
func withBody[T any](req rpc.Request, do func(id string, in T) error) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in T
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	return nil, refusal.ToRPC(do(id, in))
}

// plain answers a request that is only the person.
func plain(req rpc.Request, do func(id string) error) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	return nil, refusal.ToRPC(do(id))
}

// CompanyCreate answers rpc.users.org.company.create.
func (h *Handler) CompanyCreate(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.CompanyCreateRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	if _, err := h.svc.CreateCompany(ctx, id, in.Name); err != nil {
		return nil, refusal.ToRPC(err)
	}
	v, err := h.svc.Company(ctx, id)
	return companyView(v), refusal.ToRPC(err)
}

// CompanyGet answers rpc.users.org.company.get.
func (h *Handler) CompanyGet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	v, err := h.svc.Company(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return companyView(v), nil
}

// CompanyUpdate answers rpc.users.org.company.update.
func (h *Handler) CompanyUpdate(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.CompanyUpdateRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	v, err := h.svc.UpdateCompany(ctx, id, CompanyChange{Name: in.Name, MaxFee: in.MaxDeliveryFee, ClearFee: in.ClearFee})
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return companyView(v), nil
}

// CompanyLeave answers rpc.users.org.company.leave.
func (h *Handler) CompanyLeave(ctx context.Context, req rpc.Request) (any, error) {
	return plain(req, func(id string) error { return h.svc.LeaveCompany(ctx, id) })
}

// CompanyInvite answers rpc.users.org.company.invite.
func (h *Handler) CompanyInvite(ctx context.Context, req rpc.Request) (any, error) {
	return withBody(req, func(id string, in users.InviteRequest) error { return h.svc.InviteToCompany(ctx, id, in.Who) })
}

// CompanyRemove answers rpc.users.org.company.remove.
func (h *Handler) CompanyRemove(ctx context.Context, req rpc.Request) (any, error) {
	return withBody(req, func(id string, in users.RemoveRequest) error { return h.svc.RemoveFromCompany(ctx, id, in.UserID) })
}

// GroupCreate answers rpc.users.org.group.create.
func (h *Handler) GroupCreate(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in users.GroupCreateRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	if _, err := h.svc.CreateGroup(ctx, id, GroupInput{
		Name: in.Name, Address: in.Address, Notes: in.Notes, Lat: in.Lat, Lon: in.Lon, Free: in.Free,
	}); err != nil {
		return nil, refusal.ToRPC(err)
	}
	return h.GroupGet(ctx, req)
}

// GroupGet answers rpc.users.org.group.get.
func (h *Handler) GroupGet(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	g, members, err := h.svc.Group(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return groupView(g, members, id), nil
}

// GroupDelete answers rpc.users.org.group.delete.
func (h *Handler) GroupDelete(ctx context.Context, req rpc.Request) (any, error) {
	return plain(req, func(id string) error { return h.svc.DeleteGroup(ctx, id) })
}

// GroupLeave answers rpc.users.org.group.leave.
func (h *Handler) GroupLeave(ctx context.Context, req rpc.Request) (any, error) {
	return plain(req, func(id string) error { return h.svc.LeaveGroup(ctx, id) })
}

// GroupInvite answers rpc.users.org.group.invite.
func (h *Handler) GroupInvite(ctx context.Context, req rpc.Request) (any, error) {
	return withBody(req, func(id string, in users.InviteRequest) error { return h.svc.InviteToGroup(ctx, id, in.Who) })
}

// GroupRemove answers rpc.users.org.group.remove.
func (h *Handler) GroupRemove(ctx context.Context, req rpc.Request) (any, error) {
	return withBody(req, func(id string, in users.RemoveRequest) error { return h.svc.RemoveFromGroup(ctx, id, in.UserID) })
}

// InviteList answers rpc.users.org.invite.list.
func (h *Handler) InviteList(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	list, err := h.svc.Invites(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	out := users.InvitesResponse{Items: make([]users.Invite, 0, len(list))}
	for _, in := range list {
		out.Items = append(out.Items, users.Invite{ID: in.ID, Kind: in.Kind, Name: in.Name, ExpiresMs: in.Expires.UnixMilli()})
	}
	return out, nil
}

// InviteAccept answers rpc.users.org.invite.accept.
func (h *Handler) InviteAccept(ctx context.Context, req rpc.Request) (any, error) {
	return withBody(req, func(id string, in users.InviteIDRequest) error { return h.svc.AcceptInvite(ctx, id, in.ID) })
}

// InviteDecline answers rpc.users.org.invite.decline.
func (h *Handler) InviteDecline(ctx context.Context, req rpc.Request) (any, error) {
	return withBody(req, func(id string, in users.InviteIDRequest) error { return h.svc.DeclineInvite(ctx, id, in.ID) })
}

// Snapshot answers rpc.users.org.snapshot: where an order goes now.
func (h *Handler) Snapshot(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	g, err := h.svc.Snapshot(ctx, id)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.SnapshotResponse{
		GroupID: g.ID, GroupName: g.Name, CompanyID: g.CompanyID, CompanyName: g.CompanyName,
		Address: g.Address, Lat: g.Lat, Lon: g.Lon,
	}, nil
}

func membersView(list []Member, ownerID string) []users.Member {
	out := make([]users.Member, 0, len(list))
	for _, m := range list {
		out = append(out, users.Member{
			UserID: m.UserID, DisplayName: m.DisplayName, Nickname: m.Nickname,
			JoinedMs: m.Joined.UnixMilli(), IsOwner: m.UserID == ownerID,
		})
	}
	return out
}

func companyView(v CompanyView) users.Company {
	out := users.Company{
		ID: v.ID, Name: v.Name, IsOwner: v.IsOwner, MaxDeliveryFee: v.MaxFee, MemberCount: v.MemberCount,
	}
	if v.Members != nil {
		out.Members = membersView(v.Members, v.OwnerID)
	}
	return out
}

func groupView(g Group, members []Member, userID string) users.Group {
	return users.Group{
		ID: g.ID, Name: g.Name, Address: g.Address, Lat: g.Lat, Lon: g.Lon, Notes: g.Notes,
		CompanyID: g.CompanyID, CompanyName: g.CompanyName, IsOwner: g.OwnerID == userID,
		Members: membersView(members, g.OwnerID),
	}
}
