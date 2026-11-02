package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/nats-io/nats.go"
)

// Handler answers one kind of request. It returns the data of the reply, or
// an error. An *Error is sent as it is; any other error is a technical
// problem: the caller only gets CodeInternal, the details stay in the logs.
type Handler func(ctx context.Context, req Request) (any, error)

// Server connects handlers to subjects.
type Server struct {
	nc    *nats.Conn
	log   *slog.Logger
	queue string

	// ContextHook lets a service put request fields on the context (for
	// example the request_id for its logger). It is optional.
	ContextHook func(ctx context.Context, req Request) context.Context
}

// NewServer makes a server. All instances of a service use the same queue
// name, so NATS gives each request to only one of them.
func NewServer(nc *nats.Conn, log *slog.Logger, queue string) *Server {
	return &Server{nc: nc, log: log, queue: queue}
}

// Handle answers requests on subject with h until the connection is drained.
func (s *Server) Handle(subject string, h Handler) error {
	_, err := s.nc.QueueSubscribe(subject, s.queue, func(m *nats.Msg) {
		s.serve(m, h)
	})
	if err != nil {
		return fmt.Errorf("rpc: subscribe %s: %w", subject, err)
	}
	return nil
}

func (s *Server) serve(m *nats.Msg, h Handler) {
	resp := s.answer(m, h)
	raw, err := json.Marshal(resp)
	if err != nil {
		// the handler returned data that cannot be encoded: a bug on our side
		s.log.Error("rpc: cannot encode reply", "subject", m.Subject, "err", err)
		raw, _ = json.Marshal(Response{Error: NewError(CodeInternal, "internal error")})
	}
	if err := m.Respond(raw); err != nil {
		s.log.Error("rpc: cannot send reply", "subject", m.Subject, "err", err)
	}
}

func (s *Server) answer(m *nats.Msg, h Handler) (resp Response) {
	var req Request
	if err := json.Unmarshal(m.Data, &req); err != nil {
		return Response{Error: NewError(CodeBadRequest, "the request is not valid JSON")}
	}
	if req.V != Version {
		return Response{Error: NewError(CodeBadRequest, fmt.Sprintf("unsupported version %d", req.V))}
	}

	ctx := context.Background()
	if s.ContextHook != nil {
		ctx = s.ContextHook(ctx, req)
	}

	// A panic in one handler must not take the whole service down.
	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(ctx, "rpc: handler panicked", "subject", m.Subject, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			resp = Response{Error: NewError(CodeInternal, "internal error")}
		}
	}()

	data, err := h(ctx, req)
	if err != nil {
		var re *Error
		if errors.As(err, &re) {
			return Response{Error: re}
		}
		s.log.ErrorContext(ctx, "rpc: handler failed", "subject", m.Subject, "err", err)
		return Response{Error: NewError(CodeInternal, "internal error")}
	}
	if data == nil {
		return Response{OK: true}
	}
	enc, err := json.Marshal(data)
	if err != nil {
		s.log.ErrorContext(ctx, "rpc: cannot encode data", "subject", m.Subject, "err", err)
		return Response{Error: NewError(CodeInternal, "internal error")}
	}
	return Response{OK: true, Data: enc}
}
