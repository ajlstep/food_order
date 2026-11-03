// Package api says which subject is answered by which function: the doorway
// of core. It reads the message, calls the service and shapes the answer.
// The person is the one in the envelope, put there by the gateway.
package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"food-order/core/internal/service"
	"food-order/shared/contracts/core"
	"food-order/shared/rpc"
)

// Handlers answers the requests of core.
type Handlers struct {
	svc *service.Service
}

// New makes the handlers.
func New(svc *service.Service) *Handlers { return &Handlers{svc: svc} }

// Routes is every subject with its handler, traced.
func (h *Handlers) Routes(log *slog.Logger) map[string]rpc.Handler {
	routes := map[string]rpc.Handler{
		core.SubjectRecommendationCreate: h.createRecommendation,
		core.SubjectRecommendationGet:    h.getRecommendation,
		core.SubjectOrderCreate:          h.createOrder,
		core.SubjectOrderRate:            h.rate,
		core.SubjectOrdersList:           h.orders,
		core.SubjectStats:                h.stats,
		core.SubjectCompanyReport:        h.companyReport,
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

// who returns the person of the request, or a bad_request if there is none.
func who(req rpc.Request) (string, error) {
	if req.UserID == "" {
		return "", rpc.NewError(rpc.CodeBadRequest, "the request has no user_id")
	}
	return req.UserID, nil
}

func (h *Handlers) createRecommendation(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in core.CreateRecommendationRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.RequestRecommendation(ctx, id, in.Date)
	return out, answer(err)
}

func (h *Handlers) getRecommendation(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in core.GetRecommendationRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Recommendation(ctx, id, in.Date)
	return out, answer(err)
}

func (h *Handlers) createOrder(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in core.CreateOrderRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.CreateOrder(ctx, id, in)
	return out, answer(err)
}

func (h *Handlers) rate(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in core.RateRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Rate(ctx, id, in)
	return out, answer(err)
}

func (h *Handlers) orders(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in core.PeriodRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Orders(ctx, id, in.From, in.To)
	return core.OrdersResponse{Items: out}, answer(err)
}

func (h *Handlers) stats(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in core.PeriodRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.Stats(ctx, id, in.From, in.To)
	return out, answer(err)
}

func (h *Handlers) companyReport(ctx context.Context, req rpc.Request) (any, error) {
	id, err := who(req)
	if err != nil {
		return nil, err
	}
	var in core.PeriodRequest
	if err := rpc.Decode(req, &in); err != nil {
		return nil, err
	}
	out, err := h.svc.CompanyReport(ctx, id, in.From, in.To)
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

// traced writes a debug record for every request: what, how long, how it
// ended. The data stays out: a rating has a free comment in it.
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
