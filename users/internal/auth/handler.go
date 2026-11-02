package auth

import (
	"context"

	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
	"food-order/users/internal/refusal"
)

// Handler is the doorway of the auth zone: it reads the message, calls the
// rules, and shapes the answer. It holds no rules itself.
type Handler struct {
	resolver *Resolver
	email    *EmailService
}

// NewHandler makes the handler.
func NewHandler(r *Resolver, e *EmailService) *Handler {
	return &Handler{resolver: r, email: e}
}

// Resolve answers rpc.users.auth.resolve.
func (h *Handler) Resolve(ctx context.Context, req rpc.Request) (any, error) {
	var in users.ResolveRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	res, err := h.resolver.Resolve(ctx, Who{
		Channel: in.Channel, ExternalID: in.ExternalID, ChatRef: in.ChatRef,
		Nickname: in.Nickname, DisplayName: in.DisplayName, Language: in.Language,
	})
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.ResolveResponse{
		UserID: res.UserID, Status: res.Status, IsNew: res.IsNew, Language: res.Language, DisplayName: res.DisplayName,
	}, nil
}

// EmailRequest answers rpc.users.auth.email.request.
func (h *Handler) EmailRequest(ctx context.Context, req rpc.Request) (any, error) {
	var in users.EmailRequestRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	sent, err := h.email.Request(ctx, in.UserID, in.Email)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	return users.EmailRequestResponse{Sent: true, ValidForSecond: int(sent.ValidFor.Seconds())}, nil
}

// EmailConfirm answers rpc.users.auth.email.confirm.
func (h *Handler) EmailConfirm(ctx context.Context, req rpc.Request) (any, error) {
	var in users.EmailConfirmRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	res, err := h.email.Confirm(ctx, in.UserID, in.Code)
	if err != nil {
		return nil, refusal.ToRPC(err)
	}
	result := users.ConfirmConfirmed
	if res.Attached {
		result = users.ConfirmAttached
	}
	return users.EmailConfirmResponse{UserID: res.UserID, Result: result}, nil
}

// IdentityStatus answers rpc.users.auth.identity.status.
func (h *Handler) IdentityStatus(ctx context.Context, req rpc.Request) (any, error) {
	var in users.IdentityStatusRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	if err := h.resolver.SetStatus(ctx, in.Channel, in.ExternalID, in.Status); err != nil {
		return nil, refusal.ToRPC(err)
	}
	return nil, nil
}
