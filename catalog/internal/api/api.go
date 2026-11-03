// Package api says which subject is answered by which function: the doorway
// of catalog. Catalog knows nothing about people: the only request that names
// one is the allergen confirmation, and only to remember who confirmed.
package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"food-order/catalog/internal/service"
	"food-order/shared/contracts/catalog"
	"food-order/shared/rpc"
)

// Handlers answers the requests of catalog.
type Handlers struct {
	svc *service.Service
}

// New makes the handlers.
func New(svc *service.Service) *Handlers { return &Handlers{svc: svc} }

// Routes is every subject with its handler, traced.
func (h *Handlers) Routes(log *slog.Logger) map[string]rpc.Handler {
	routes := map[string]rpc.Handler{
		catalog.SubjectMenuGet:          h.menu,
		catalog.SubjectMenuImport:       h.importMenu,
		catalog.SubjectDishesGet:        h.dishes,
		catalog.SubjectSimilar:          h.similar,
		catalog.SubjectAllergensConfirm: h.confirmAllergens,
		catalog.SubjectPartnersList:     h.partners,
	}
	for subject, handler := range routes {
		routes[subject] = traced(log, subject, handler)
	}
	return routes
}

// Register connects the routes to the server.
func (h *Handlers) Register(srv *rpc.Server, log *slog.Logger) error {
	for subject, handler := range h.Routes(log) {
		if err := srv.Handle(subject, handler); err != nil {
			return err
		}
	}
	return nil
}

func (h *Handlers) menu(ctx context.Context, req rpc.Request) (any, error) {
	var in catalog.MenuGetRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Menu(ctx, in.Date)
	return out, answer(err)
}

func (h *Handlers) importMenu(ctx context.Context, req rpc.Request) (any, error) {
	var in catalog.MenuFetched
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Import(ctx, in)
	return out, answer(err)
}

func (h *Handlers) dishes(ctx context.Context, req rpc.Request) (any, error) {
	var in catalog.DishesGetRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Dishes(ctx, in.IDs, in.Date)
	return out, answer(err)
}

func (h *Handlers) similar(ctx context.Context, req rpc.Request) (any, error) {
	var in catalog.SimilarRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Similar(ctx, in)
	return out, answer(err)
}

func (h *Handlers) confirmAllergens(ctx context.Context, req rpc.Request) (any, error) {
	if req.UserID == "" {
		return nil, rpc.NewError(rpc.CodeBadRequest, "the request has no user_id")
	}
	var in catalog.ConfirmAllergensRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.ConfirmAllergens(ctx, req.UserID, in.DishID, in.Allergens)
	return out, answer(err)
}

func (h *Handlers) partners(ctx context.Context, req rpc.Request) (any, error) {
	var in catalog.PartnersListRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Partners(ctx, in.Date)
	return out, answer(err)
}

// answer turns a refusal of the service into what crosses the wire, with
// the words for the person. Any other error stays as it is: rpc.Server logs
// it and answers only "internal".
func answer(err error) error {
	var refused *service.Error
	if errors.As(err, &refused) {
		return rpc.Refuse(refused.Code, refused.Message)
	}
	return err
}

// traced writes a debug record for every request: what, how long, how it ended.
func traced(log *slog.Logger, subject string, next rpc.Handler) rpc.Handler {
	return func(ctx context.Context, req rpc.Request) (any, error) {
		start := time.Now()
		data, err := next(ctx, req)
		outcome := "ok"
		var re *rpc.Error
		switch {
		case errors.As(err, &re):
			outcome = re.Code
		case err != nil:
			outcome = "failed"
		}
		log.DebugContext(ctx, "rpc handled", "subject", subject, "outcome", outcome, "ms", time.Since(start).Milliseconds())
		return data, err
	}
}
