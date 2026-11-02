package access

import (
	"context"
	"time"

	"food-order/gateway/internal/requestid"
	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
)

// RPCResolver asks users over NATS.
type RPCResolver struct {
	client  *rpc.Client
	timeout time.Duration
}

// NewRPCResolver makes the resolver; timeout is how long users may take.
func NewRPCResolver(client *rpc.Client, timeout time.Duration) *RPCResolver {
	return &RPCResolver{client: client, timeout: timeout}
}

// Resolve sends rpc.users.auth.resolve.
func (r *RPCResolver) Resolve(ctx context.Context, in users.ResolveRequest) (users.ResolveResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	var out users.ResolveResponse
	err := r.client.Call(ctx, users.SubjectAuthResolve, requestid.From(ctx), in, &out)
	return out, err
}
