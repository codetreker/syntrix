package ctxkeys

import (
	"context"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
	"testing"
)

func TestRequestContextPropagation(t *testing.T) {
	original := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "unchanged", "x-request-id", "old"))
	ctx := context.WithValue(original, KeyRequestID, "request-123")
	outgoing := OutgoingRequestContext(ctx)
	md, ok := metadata.FromOutgoingContext(outgoing)
	require.True(t, ok)
	require.Equal(t, []string{"request-123"}, md.Get("x-request-id"))
	require.Equal(t, []string{"unchanged"}, md.Get("authorization"))
	prior, _ := metadata.FromOutgoingContext(original)
	require.Equal(t, []string{"old"}, prior.Get("x-request-id"))
	incoming := IncomingRequestContext(metadata.NewIncomingContext(context.Background(), md))
	require.Equal(t, "request-123", RequestID(incoming))
	empty := context.Background()
	require.Equal(t, empty, OutgoingRequestContext(empty))
	require.Equal(t, "", RequestID(IncomingRequestContext(empty)))
	for _, values := range [][]string{{""}, {"one", "two"}} {
		require.Equal(t, "", RequestID(IncomingRequestContext(metadata.NewIncomingContext(empty, metadata.MD{"x-request-id": values}))))
	}
}
