package ctxkeys

import (
	"context"
	"google.golang.org/grpc/metadata"
)

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(KeyRequestID).(string)
	return id
}

// OutgoingRequestContext carries the HTTP request correlation through a gRPC hop
// while preserving unrelated outgoing metadata and the caller's context ownership.
func OutgoingRequestContext(ctx context.Context) context.Context {
	id := RequestID(ctx)
	if id == "" {
		return ctx
	}
	existing, _ := metadata.FromOutgoingContext(ctx)
	copied := existing.Copy()
	copied.Set("x-request-id", id)
	return metadata.NewOutgoingContext(ctx, copied)
}

func IncomingRequestContext(ctx context.Context) context.Context {
	ids := metadata.ValueFromIncomingContext(ctx, "x-request-id")
	if len(ids) != 1 || ids[0] == "" {
		return ctx
	}
	return context.WithValue(ctx, KeyRequestID, ids[0])
}
