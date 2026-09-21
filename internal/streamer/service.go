package streamer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/syntrixbase/syntrix/api/gen/streamer/v1"
	"github.com/syntrixbase/syntrix/internal/helper"
	"github.com/syntrixbase/syntrix/internal/puller"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	celengine "github.com/syntrixbase/syntrix/internal/streamer/cel"
	"github.com/syntrixbase/syntrix/internal/streamer/manager"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc"
)

// ServerConfig configures the Streamer service.
type ServerConfig struct {
	// PullerAddr is the address of the Puller gRPC server.
	// If empty, Streamer runs in standalone mode without event ingestion.
	PullerAddr string `yaml:"puller_addr"`

	// SendTimeout is the maximum time to wait for a gateway to accept an event.
	// If the timeout is exceeded, the gateway is considered slow and should be disconnected.
	// Default: 5 seconds.
	SendTimeout time.Duration `yaml:"send_timeout"`

	// pullerClient is an optional pre-configured Puller client.
	// If provided, it will be used instead of creating a new client from PullerAddr.
	// This is primarily for testing purposes.
	pullerClient puller.Service
}

func DefaultServiceConfig() ServerConfig {
	return ServerConfig{
		PullerAddr:  "http://localhost:9000",
		SendTimeout: 5 * time.Second,
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *ServerConfig) ApplyDefaults() {
	defaults := DefaultServiceConfig()
	if c.PullerAddr == "" {
		c.PullerAddr = defaults.PullerAddr
	}
	if c.SendTimeout == 0 {
		c.SendTimeout = defaults.SendTimeout
	}
}

// ServiceConfigOption is a functional option for ServiceConfig.
type ServiceConfigOption func(*ServerConfig)

// WithPullerClient sets a pre-configured Puller client (for testing).
func WithPullerClient(client puller.Service) ServiceConfigOption {
	return func(c *ServerConfig) {
		c.pullerClient = client
	}
}

// streamerService implements the Streamer gRPC service and StreamerServer interface.
type streamerService struct {
	pb.UnimplementedStreamerServiceServer

	config  ServerConfig
	manager *manager.Manager

	// streams maps gatewayID to its localStream
	streams   map[string]*localStream
	streamsMu sync.RWMutex

	// Puller integration (created internally on Start)
	pullerClient puller.Service
	progress     string // last processed progress marker

	ctx    context.Context
	cancel context.CancelFunc

	logger *slog.Logger
}

// NewService creates a new streamerService and returns the StreamerServer interface.
func NewService(config ServerConfig, logger *slog.Logger, opts ...ServiceConfigOption) (StreamerServer, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Apply functional options
	for _, opt := range opts {
		opt(&config)
	}

	// Set default timeout if not configured
	if config.SendTimeout == 0 {
		config.SendTimeout = 5 * time.Second
	}

	celCompiler, err := celengine.NewCompiler()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL compiler: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &streamerService{
		config:  config,
		manager: manager.New(manager.WithCELCompiler(celCompiler)),
		streams: make(map[string]*localStream),
		ctx:     ctx,
		cancel:  cancel,
		logger:  logger.With("component", "streamer"),
	}, nil
}

// Stream implements the Service interface.
// Returns a bidirectional stream for Gateway communication.
func (s *streamerService) Stream(ctx context.Context) (Stream, error) {
	gatewayID := uuid.New().String()
	s.logger.Info("New local stream", "gatewayID", gatewayID)

	ls := newLocalStream(ctx, gatewayID, s)

	s.streamsMu.Lock()
	if err := s.ctx.Err(); err != nil {
		s.streamsMu.Unlock()
		ls.close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		s.streamsMu.Unlock()
		ls.close()
		return nil, err
	}
	s.streams[gatewayID] = ls
	s.streamsMu.Unlock()

	// Monitor context and clean up when done
	go func() {
		<-ls.ctx.Done()
		s.removeStream(gatewayID)
	}()

	return ls, nil
}

// Start begins the streamer service, connecting to Puller and consuming events.
func (s *streamerService) Start(ctx context.Context) error {
	// Use pre-configured client if available (e.g., for testing)
	if s.config.pullerClient != nil {
		s.pullerClient = s.config.pullerClient
	} else if s.config.PullerAddr != "" {
		// Create Puller client from address
		s.logger.Info("Streamer service starting, connecting to Puller", "addr", s.config.PullerAddr)
		pullerClient, err := puller.NewClient(s.config.PullerAddr, s.logger)
		if err != nil {
			return fmt.Errorf("failed to create puller client: %w", err)
		}
		s.pullerClient = pullerClient
	}

	// If no Puller client configured, run in standalone mode
	if s.pullerClient == nil {
		s.logger.Info("Streamer service starting without Puller (standalone mode)")
		return nil
	}

	// Subscribe to events (auto-reconnects on failures)
	eventChan := s.pullerClient.Subscribe(ctx, "streamer", s.progress)

	go s.consumePullerEvents(ctx, eventChan)
	s.logger.Info("Started consuming from Puller")
	return nil
}

// consumePullerEvents consumes events from Puller and processes them.
func (s *streamerService) consumePullerEvents(ctx context.Context, eventChan <-chan *puller.Event) {
	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Stopping Puller consumption")
			return
		case <-s.ctx.Done():
			s.logger.Info("Stopping Puller consumption (service stopped)")
			return
		case evt, ok := <-eventChan:
			if !ok {
				s.logger.Info("Puller event channel closed")
				return
			}
			if evt.Change == nil {
				continue // Skip events without change data
			}

			s.logger.Debug("Streamer: received event from puller",
				"eventID", evt.Change.EventID,
				"op", evt.Change.OpType,
				"backend", evt.Change.Backend,
			)

			if event, err := events.Transform(evt); err == nil {
				if err := s.ProcessEvent(event); err != nil {
					s.logger.Error("Failed to process event", "error", err, "eventID", evt.Change.EventID)
				}
			}
			s.progress = evt.Progress
		}
	}
}

// Stop gracefully shuts down the streamer service.
func (s *streamerService) Stop(ctx context.Context) error {
	s.logger.Info("Streamer service stopping")
	s.cancel()

	s.streamsMu.Lock()
	for id, ls := range s.streams {
		ls.close()
		delete(s.streams, id)
	}
	s.streamsMu.Unlock()

	return nil
}

// removeStream removes a stream from the service.
func (s *streamerService) removeStream(gatewayID string) {
	s.streamsMu.Lock()
	if ls, ok := s.streams[gatewayID]; ok {
		ls.close()
		delete(s.streams, gatewayID)
	}
	s.streamsMu.Unlock()

	s.manager.UnregisterGateway(gatewayID)
	s.logger.Info("Stream removed", "gatewayID", gatewayID)
}

// --- gRPC Server Methods ---

// GRPCStream implements the gRPC bidirectional streaming endpoint.
// This is called by gRPC framework, not directly by users.
func (s *streamerService) GRPCStream(stream grpc.BidiStreamingServer[pb.GatewayMessage, pb.StreamerMessage]) error {
	gatewayID := uuid.New().String()
	s.logger.Info("New gRPC stream connection", "gatewayID", gatewayID)

	// Create a gRPC stream adapter
	gs := newGRPCStreamAdapter(stream.Context(), gatewayID, stream, s)

	s.streamsMu.Lock()
	s.streams[gatewayID] = gs.localStream
	s.streamsMu.Unlock()
	defer s.removeStream(gatewayID)

	return gs.run()
}

// ProcessEvent processes a single SyntrixChangeEvent, matching it to subscriptions
// and delivering to streams.
// Backpressure: Uses blocking send with timeout. If a gateway cannot accept
// the event within the timeout, it is considered slow and will be disconnected.
func (s *streamerService) ProcessEvent(event events.SyntrixChangeEvent) error {
	if event.Document == nil {
		return nil // Skip events without document
	}

	s.logger.Debug("Streamer: processing event",
		"database", event.Database,
		"collection", event.Document.Collection,
	)

	doc := helper.FlattenStorageDocument(event.Document)
	matches := s.manager.Match(event.Database, event.Document.Collection, doc.GetID(), doc)

	if len(matches) == 0 {
		return nil
	}

	s.streamsMu.RLock()
	defer s.streamsMu.RUnlock()

	for gatewayID, subIDs := range matches {
		ls, ok := s.streams[gatewayID]
		if !ok {
			continue
		}

		// Convert SyntrixChangeEvent to EventDelivery
		delivery := syntrixEventToDelivery(event, doc, subIDs)

		// Blocking send with timeout - no drop allowed
		select {
		case ls.outgoing <- delivery:
			// Successfully sent
		case <-time.After(s.config.SendTimeout):
			// Gateway is too slow, mark for disconnection
			s.logger.Warn("Gateway too slow, timeout sending event",
				"gatewayID", gatewayID,
				"timeout", s.config.SendTimeout)
			go s.removeStream(gatewayID)
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}

	return nil
}

// ProcessEventJSON processes a JSON-encoded StoreChangeEvent from Puller.
// It transforms the event to SyntrixChangeEvent before processing.
func (s *streamerService) ProcessEventJSON(data []byte) error {
	var pEvent events.PullerEvent
	if err := json.Unmarshal(data, &pEvent); err != nil {
		return fmt.Errorf("failed to unmarshal event: %w", err)
	}

	event, err := events.Transform(&pEvent)
	if err != nil {
		if errors.Is(err, events.ErrDeleteOPIgnored) {
			return nil // Delete operations are handled differently
		}
		return fmt.Errorf("failed to transform event: %w", err)
	}

	return s.ProcessEvent(event)
}

// Compile-time checks
var _ Service = (*streamerService)(nil)
var _ StreamerServer = (*streamerService)(nil)
var _ EventProcessor = (*streamerService)(nil)
var _ subscriptionHandler = (*streamerService)(nil)

// --- subscriptionHandler implementation ---

// subscribe implements subscriptionHandler for localStream.
func (s *streamerService) subscribe(gatewayID, database, collection string, filters []model.Filter) (string, error) {
	subID := uuid.New().String()
	protoReq := &pb.SubscribeRequest{
		SubscriptionId: subID,
		Database:       database,
		Collection:     collection,
		Filters:        filtersToProto(filters),
	}

	resp, err := s.manager.Subscribe(gatewayID, protoReq)
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("subscribe failed: %s", resp.Error)
	}
	return resp.SubscriptionId, nil
}

// unsubscribe implements subscriptionHandler for localStream.
func (s *streamerService) unsubscribe(subscriptionID string) error {
	return s.manager.Unsubscribe(subscriptionID)
}
