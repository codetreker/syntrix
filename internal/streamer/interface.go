// Package streamer provides centralized fan-out and subscription matching.
// It replaces the pull-based CSP package with a subscription-based model.
package streamer

import (
	"context"
	"fmt"

	pb "github.com/syntrixbase/syntrix/api/gen/streamer/v1"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc"
)

// Service is the Streamer service interface.
// Both local implementation and gRPC client implement this interface,
// enabling single-process mode without network or serialization overhead.
type Service interface {
	// Stream establishes a bidirectional stream for subscriptions and events.
	// Used by Gateway to register interest and receive updates.
	Stream(ctx context.Context) (Stream, error)
}

// Registration belongs to one transport generation of the actual Stream object.
// Generation is an ownership fence, not a replication checkpoint.
type Registration struct {
	ID         string
	Generation uint64
}

// StreamStatus atomically captures the current state and its next-change signal.
// Callers must obtain a fresh snapshot after Changed closes. Terminal streams
// never reconnect; a replacement Stream has an independent ownership lifetime.
type StreamStatus struct {
	State      ConnectionState
	Generation uint64
	Terminal   bool
	Err        error
	Changed    <-chan struct{}
}

// Stream is a bidirectional stream for Gateway <-> Streamer communication.
// This interface abstracts the underlying transport (local or gRPC).
//
// Usage:
//
//	stream, _ := service.Stream(ctx)
//	defer stream.Close()
//	registration, _ := stream.Subscribe(ctx, "database1", "users", []Filter{{Field: "status", Op: "==", Value: "active"}})
//	for {
//	    delivery, err := stream.Recv()
//	    if err != nil { break }
//	    // process delivery.Event
//	}
type Stream interface {
	// Subscribe creates a new subscription for the specified database and collection.
	// Returns a registration acknowledged by the current generation. Context
	// cancellation applies to setup; successful registrations require Unsubscribe.
	// For document ID match, use Filter{Field: "id", Op: model.OpEq, Value: docID}.
	Subscribe(ctx context.Context, database, collection string, filters []model.Filter) (Registration, error)

	Status() StreamStatus

	// Unsubscribe removes a subscription by ID.
	Unsubscribe(subscriptionID string) error

	// Recv receives an EventDelivery from the Streamer.
	// Blocks until an event is available or an error occurs.
	// Returns io.EOF when the stream is closed.
	Recv() (*EventDelivery, error)

	// Close closes the stream and releases resources.
	Close() error
}

// StreamerServer is the server-side interface for managing the Streamer service.
// This extends Service with lifecycle operations.
type StreamerServer interface {
	Service

	// Start begins the streamer service, connecting to Puller and
	// processing events.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the streamer service.
	Stop(ctx context.Context) error
}

// EventProcessor is an internal interface for processing events from Puller.
// This is implemented by streamerService and used by PullerIntegration.
type EventProcessor interface {
	// ProcessEvent processes a SyntrixChangeEvent (already transformed from StoreChangeEvent).
	ProcessEvent(e events.SyntrixChangeEvent) error

	// ProcessEventJSON processes a JSON-encoded StoreChangeEvent from Puller.
	// It transforms the event to SyntrixChangeEvent internally.
	ProcessEventJSON(data []byte) error
}

// subscriptionHandler is the internal interface for localStream to manage subscriptions.
// This decouples localStream from the concrete streamerService implementation.
type subscriptionHandler interface {
	// subscribe registers a new subscription and returns the subscription ID.
	subscribe(gatewayID, database, collection string, filters []model.Filter) (subscriptionID string, err error)

	// unsubscribe removes a subscription by ID.
	unsubscribe(subscriptionID string) error
}

// grpcServerAdapter adapts StreamerServer to pb.StreamerServiceServer.
// This bridges the internal Service interface with the gRPC proto interface.
type grpcServerAdapter struct {
	pb.UnimplementedStreamerServiceServer
	service *streamerService
}

// Stream implements pb.StreamerServiceServer.Stream by delegating to GRPCStream.
func (a *grpcServerAdapter) Stream(stream grpc.BidiStreamingServer[pb.GatewayMessage, pb.StreamerMessage]) error {
	return a.service.GRPCStream(stream)
}

// NewGRPCServer creates a gRPC server for the Streamer service.
// The server implements pb.StreamerServiceServer and wraps the StreamerServer interface.
// Returns an error if the provided srv is not a *streamerService instance.
func NewGRPCServer(srv StreamerServer) (pb.StreamerServiceServer, error) {
	// Type assert to get the concrete service implementation
	svc, ok := srv.(*streamerService)
	if !ok {
		return nil, fmt.Errorf("NewGRPCServer requires a *streamerService instance, got %T", srv)
	}
	return &grpcServerAdapter{service: svc}, nil
}
