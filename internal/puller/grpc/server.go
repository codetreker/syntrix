package grpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/core"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// EventSource provides the event handler setter interface.
// The handler function receives events from the puller for distribution.
type EventSource interface {
	SetEventHandler(handler func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error)
	Replay(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error)
}

type boundarySource interface {
	BootstrapBoundary(context.Context) (string, error)
	ValidateBoundary(context.Context, string) error
	ReplayBoundary(context.Context, string, bool) (events.Iterator, error)
}

// Server implements the PullerService gRPC interface.
// It is designed to be registered with a unified gRPC server.
type Server struct {
	pullerv1.UnimplementedPullerServiceServer

	cfg         config.GRPCConfig
	eventSource EventSource
	logger      *slog.Logger
	subs        *core.SubscriberManager

	// eventChan receives events from the puller for broadcasting
	eventChan chan *events.StoreChangeEvent

	// ctx controls the server lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// mu protects server state
	mu sync.RWMutex

	// initialized indicates if the event processing loop is running
	initialized bool
}

// NewServer creates a new Puller gRPC service implementation.
// The returned server implements pullerv1.PullerServiceServer and should be
// registered with a gRPC server using pullerv1.RegisterPullerServiceServer.
func NewServer(cfg config.GRPCConfig, eventSource EventSource, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MaxConnections <= 0 {
		cfg.MaxConnections = config.DefaultConfig().GRPC.MaxConnections
	}

	channelSize := cfg.ChannelSize
	if channelSize <= 0 {
		channelSize = 10000
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		cfg:         cfg,
		eventSource: eventSource,
		logger:      logger.With("component", "puller-grpc"),
		subs:        core.NewSubscriberManager(logger),
		eventChan:   make(chan *events.StoreChangeEvent, channelSize),
		ctx:         ctx,
		cancel:      cancel,
	}
}

// Init initializes the server by setting up the event handler and starting
// the event processing loop. This must be called after registering the server
// with a gRPC server and before the unified server starts.
func (s *Server) Init() {
	s.mu.Lock()
	if s.initialized {
		s.mu.Unlock()
		return
	}
	s.initialized = true
	s.mu.Unlock()

	// Set up event handler to receive events from puller
	s.eventSource.SetEventHandler(func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error {
		// Ensure backend name is set
		if event.Backend == "" {
			event.Backend = backendName
		}
		select {
		case s.eventChan <- event:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	s.logger.Info("Puller gRPC service initialized")

	// Start event processing loop
	go s.processEvents()
}

// processEvents reads events from the channel and broadcasts them to subscribers.
func (s *Server) processEvents() {
	for {
		select {
		case event, ok := <-s.eventChan:
			if !ok {
				return
			}
			s.subs.Broadcast(event)
		case <-s.ctx.Done():
			return
		}
	}
}

// Shutdown gracefully shuts down the server, closing all subscribers.
// This should be called when the unified server is stopping.
func (s *Server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initialized {
		return
	}

	// Signal shutdown
	s.cancel()

	// Close all subscribers
	s.subs.CloseAll()

	s.logger.Info("Puller gRPC service shut down")
}

func (s *Server) BootstrapBoundary(ctx context.Context, _ *pullerv1.BootstrapBoundaryRequest) (*pullerv1.BoundaryResponse, error) {
	source, ok := s.eventSource.(boundarySource)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "source boundaries are unavailable")
	}
	progress, err := source.BootstrapBoundary(ctx)
	if err != nil {
		code := codes.FailedPrecondition
		if errors.Is(err, core.ErrCaptureUnavailable) {
			code = codes.Unavailable
		}
		return nil, status.Errorf(code, "bootstrap boundary: %v", err)
	}
	response := &pullerv1.BoundaryResponse{Progress: progress}
	if err := validateResponseSize(response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Server) ValidateBoundary(ctx context.Context, req *pullerv1.ValidateBoundaryRequest) (*pullerv1.BoundaryResponse, error) {
	source, ok := s.eventSource.(boundarySource)
	if !ok {
		return nil, status.Error(codes.Unimplemented, "source boundaries are unavailable")
	}
	if err := source.ValidateBoundary(ctx, req.Progress); err != nil {
		code := codes.FailedPrecondition
		if errors.Is(err, core.ErrCaptureUnavailable) {
			code = codes.Unavailable
		}
		return nil, status.Errorf(code, "invalid boundary: %v", err)
	}
	response := &pullerv1.BoundaryResponse{Progress: req.Progress}
	if err := validateResponseSize(response); err != nil {
		return nil, err
	}
	return response, nil
}

// Subscribe implements the PullerService Subscribe RPC.
func (s *Server) Subscribe(req *pullerv1.SubscribeRequest, stream pullerv1.PullerService_SubscribeServer) error {
	ctx := stream.Context()

	// Parse the 'after' progress marker
	after, err := cursor.DecodeProgressMarker(req.GetAfter())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid 'after' progress marker: %v", err)
	}

	// Serialize capacity checks and registration with other admissions and Shutdown.
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		return nil
	}
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return status.Error(codes.Unavailable, "puller service is shut down")
	}
	active := s.subs.Count()
	if active >= s.cfg.MaxConnections {
		s.mu.Unlock()
		s.logger.Warn("subscription rejected",
			"consumerId", req.GetConsumerId(),
			"code", codes.ResourceExhausted.String(),
			"activeSubscriptions", active,
			"limit", s.cfg.MaxConnections,
		)
		return status.Error(codes.ResourceExhausted, "puller subscription limit reached")
	}

	channelSize := s.cfg.ChannelSize
	if channelSize <= 0 {
		channelSize = 10000
	}

	sub, err := core.NewSubscriber(req.GetConsumerId(), after, req.GetCoalesceOnCatchUp(), channelSize)
	if err != nil {
		s.mu.Unlock()
		return status.Errorf(codes.InvalidArgument, "invalid subscription progress: %v", err)
	}
	s.subs.Add(sub)
	s.mu.Unlock()
	defer s.subs.Remove(sub)

	s.logger.Info("subscriber connected",
		"consumerId", sub.ID,
		"after", req.GetAfter(),
		"coalesceOnCatchUp", sub.CoalesceOnCatchUp,
	)

	var source boundarySource
	if req.RequireReady {
		var ok bool
		source, ok = s.eventSource.(boundarySource)
		if !ok {
			return status.Error(codes.Unimplemented, "verified subscriptions are unavailable")
		}
		if err := source.ValidateBoundary(ctx, req.After); err != nil {
			code := codes.FailedPrecondition
			if errors.Is(err, core.ErrCaptureUnavailable) {
				code = codes.Unavailable
			}
			return status.Errorf(code, "invalid subscription boundary: %v", err)
		}
	}
	readySent := false

	// Mode: "catchup" or "live"
	mode := "catchup"
	if req.GetAfter() == "" {
		mode = "live"
		s.logger.Info("starting in live mode (no history requested)", "consumerId", sub.ID)
	}

	// Setup heartbeat ticker if enabled
	heartbeatInterval := s.cfg.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = 30 * time.Second // Default to 30s
	}
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	shouldDrain := false

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("subscriber disconnected", "consumerId", sub.ID)
			return nil
		case <-sub.Done():
			s.logger.Info("subscriber closed", "consumerId", sub.ID)
			return status.Error(codes.Canceled, "subscription closed")
		default:
		}

		if mode == "catchup" {
			// Drain channel to make space for new events if we are recovering from overflow
			if shouldDrain {
				drainChannel(sub.Events())
				shouldDrain = false
			}
			sub.GetAndResetOverflow() // Clear overflow flag

			// Start replay
			var iter events.Iterator
			var err error
			if source != nil {
				iter, err = source.ReplayBoundary(ctx, sub.CurrentProgress().Encode(), sub.CoalesceOnCatchUp)
			} else {
				iter, err = s.eventSource.Replay(ctx, sub.CurrentProgress().Positions, sub.CoalesceOnCatchUp)
			}
			if err != nil {
				s.logger.Error("failed to start replay", "error", err)
				return status.Errorf(codes.Internal, "failed to start replay: %v", err)
			}

			// Replay loop
			replayCount := 0
			for iter.Next() {
				evt := iter.Event()
				replayCount++
				s.logger.Debug("Replay iter.Next()", "replayCount", replayCount, "eventID", evt.EventID, "backend", evt.Backend, "clusterTime", evt.ClusterTime)

				// Deduplication: check if event is already sent
				// This is crucial if Replay restarts or if ScanFrom is inclusive
				if !sub.ShouldSend(evt.Backend, evt.EventID, evt.ClusterTime) {
					s.logger.Debug("Skipping event (already sent)", "eventID", evt.EventID)
					continue
				}

				if err := s.sendEvent(stream, sub, evt.Backend, evt); err != nil {
					iter.Close()
					return err
				}
			}
			replayErr := iter.Err()
			closeErr := iter.Close()
			s.logger.Debug("Replay finished", "totalReplayCount", replayCount, "iterErr", replayErr)

			if err := errors.Join(replayErr, closeErr); err != nil {
				s.logger.Error("replay error", "error", err)
				return status.Errorf(codes.Internal, "replay error: %v", err)
			}

			// Check if we overflowed during replay
			if sub.GetAndResetOverflow() {
				s.logger.Info("subscriber overflowed during replay, continuing catchup", "consumerId", sub.ID)
				shouldDrain = true
				continue
			}

			// Caught up
			mode = "live"
			s.logger.Info("subscriber caught up, switching to live mode", "consumerId", sub.ID, "replayedEvents", replayCount)
			// Reset heartbeat ticker when entering live mode
			heartbeatTicker.Reset(heartbeatInterval)

		} else {
			if req.RequireReady && !readySent {
				if err := sendResponse(stream, &pullerv1.PullerEvent{Ready: true, Progress: sub.CurrentProgress().Encode()}); err != nil {
					return err
				}
				readySent = true
			}
			// Live loop
			select {
			case <-ctx.Done():
				s.logger.Info("subscriber disconnected", "consumerId", sub.ID)
				return nil

			case <-sub.Done():
				s.logger.Info("subscriber closed", "consumerId", sub.ID)
				return status.Error(codes.Canceled, "subscription closed")

			case <-heartbeatTicker.C:
				if source != nil {
					if err := source.ValidateBoundary(ctx, sub.CurrentProgress().Encode()); err != nil {
						code := codes.FailedPrecondition
						if errors.Is(err, core.ErrCaptureUnavailable) {
							code = codes.Unavailable
						}
						return status.Errorf(code, "subscription boundary unavailable: %v", err)
					}
				}
				// Send heartbeat (PullerEvent with nil ChangeEvent)
				if err := s.sendHeartbeat(stream, sub); err != nil {
					return err
				}

			case evt := <-sub.Events():
				// Check for overflow, but don't switch immediately.
				// We need to process the current event 'evt' first to ensure we don't drop it.
				// If we switch immediately, 'evt' would be lost.
				hasOverflow := sub.GetAndResetOverflow()

				if evt != nil && sub.ShouldSend(evt.Backend, evt.EventID, evt.ClusterTime) {
					if err := s.sendEvent(stream, sub, evt.Backend, evt); err != nil {
						return err
					}
				}

				if hasOverflow {
					s.logger.Info("subscriber overflowed, switching to catchup", "consumerId", sub.ID)
					mode = "catchup"
					shouldDrain = true
					continue
				}
			}
		}
	}
}

func drainChannel(ch <-chan *events.StoreChangeEvent) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// sendEvent sends a single event to the stream and updates subscriber position.
func (s *Server) sendEvent(stream pullerv1.PullerService_SubscribeServer, sub *core.Subscriber, backend string, evt *events.StoreChangeEvent) error {
	// Convert to gRPC event
	changeEvt, err := s.convertEvent(backend, evt)
	if err != nil {
		s.logger.Error("failed to convert event", "error", err)
		return fmt.Errorf("convert puller event: %w", err)
	}

	position := sub.CurrentProgress()
	position.SetPosition(backend, evt.EventID)

	pullerEvt := &pullerv1.PullerEvent{
		ChangeEvent: changeEvt,
		Progress:    position.Encode(),
	}

	// Send to client
	if err := sendResponse(stream, pullerEvt); err != nil {
		s.logger.Error("failed to send event", "error", err, "consumerId", sub.ID)
		return err
	}
	sub.UpdatePosition(backend, evt.EventID, evt.ClusterTime)
	return nil
}

// sendHeartbeat sends a heartbeat event to keep the connection alive.
// Heartbeat is a PullerEvent with nil ChangeEvent but includes current progress.
func (s *Server) sendHeartbeat(stream pullerv1.PullerService_SubscribeServer, sub *core.Subscriber) error {
	pullerEvt := &pullerv1.PullerEvent{
		ChangeEvent: nil, // nil indicates heartbeat
		Progress:    sub.CurrentProgress().Encode(),
	}

	if err := sendResponse(stream, pullerEvt); err != nil {
		s.logger.Error("failed to send heartbeat", "error", err, "consumerId", sub.ID)
		return err
	}
	return nil
}

// convertEvent converts a ChangeEvent to a gRPC ChangeEvent.
func (s *Server) convertEvent(backend string, evt *events.StoreChangeEvent) (*pullerv1.ChangeEvent, error) {
	var fullDocBytes []byte
	var updateDescBytes []byte
	var err error

	if evt.FullDocument != nil {
		fullDocBytes, err = events.MarshalDocument(evt.FullDocument)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal full document: %w", err)
		}
	}

	if evt.UpdateDesc != nil {
		updateDescBytes, err = events.MarshalUpdateDescription(evt.UpdateDesc)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal update description: %w", err)
		}
	}

	if err := events.ValidateEncodedEventSize(evt, fullDocBytes, updateDescBytes); err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}

	var txnNumber int64
	if evt.TxnNumber != nil {
		txnNumber = *evt.TxnNumber
	}

	return &pullerv1.ChangeEvent{
		EventId:    evt.EventID,
		Database:   evt.Database,
		MgoColl:    evt.MgoColl,
		MgoDocId:   evt.MgoDocID,
		OpType:     string(evt.OpType),
		FullDoc:    fullDocBytes,
		UpdateDesc: updateDescBytes,
		ClusterTime: &pullerv1.ClusterTime{
			T: evt.ClusterTime.T,
			I: evt.ClusterTime.I,
		},
		Timestamp: evt.Timestamp,
		TxnNumber: txnNumber,
		Backend:   backend,
	}, nil
}

func validateResponseSize(message proto.Message) error {
	if size := proto.Size(message); size > events.MaxRPCBytes {
		return status.Errorf(codes.FailedPrecondition, "complete puller response exceeds 65 MiB (encoded bytes: %d)", size)
	}
	return nil
}

func sendResponse(stream pullerv1.PullerService_SubscribeServer, message *pullerv1.PullerEvent) error {
	if err := validateResponseSize(message); err != nil {
		return err
	}
	return stream.Send(message)
}

// SubscriberCount returns the number of active subscribers.
func (s *Server) SubscriberCount() int {
	return s.subs.Count()
}
