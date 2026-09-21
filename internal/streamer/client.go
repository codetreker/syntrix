package streamer

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/syntrixbase/syntrix/api/gen/streamer/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ConnectionState represents the current connection state.
type ConnectionState int

const (
	// StateDisconnected indicates the client is not connected.
	StateDisconnected ConnectionState = iota
	// StateConnecting indicates the client is establishing a connection.
	StateConnecting
	// StateConnected indicates the client is connected and receiving events.
	StateConnected
	// StateReconnecting indicates the client is attempting to reconnect.
	StateReconnecting
)

// String returns a string representation of the connection state.
func (s ConnectionState) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	default:
		return "unknown"
	}
}

// StateChangeCallback is called when connection state changes.
type StateChangeCallback func(state ConnectionState, err error)

// ClientConfig configures the Streamer client.
type ClientConfig struct {
	// StreamerAddr is the address of the Streamer gRPC server.
	StreamerAddr string `yaml:"streamer_addr"`

	// InitialBackoff is the initial wait time before first reconnect attempt.
	// Defaults to 1 second.
	InitialBackoff time.Duration `yaml:"initial_backoff"`

	// MaxBackoff is the maximum wait time between reconnect attempts.
	// Defaults to 30 seconds.
	MaxBackoff time.Duration `yaml:"max_backoff"`

	// BackoffMultiplier is the factor by which backoff increases after each failure.
	// Defaults to 2.0.
	BackoffMultiplier float64 `yaml:"backoff_multiplier"`

	// MaxRetries is the maximum number of consecutive reconnect attempts.
	// Set to 0 for unlimited retries. Defaults to 0 (unlimited).
	MaxRetries int `yaml:"max_retries"`

	// HeartbeatInterval is the time between heartbeat messages.
	// If > 0, client sends periodic heartbeats to detect stale connections.
	// Defaults to 30 seconds.
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`

	// ActivityTimeout is the maximum time to wait for any message before
	// considering the connection stale. Defaults to 90 seconds.
	ActivityTimeout time.Duration `yaml:"activity_timeout"`

	// OnStateChange reports the latest connection state. Slow observers may coalesce
	// transitions; use the actual Stream.Status ownership fence for registrations.
	OnStateChange StateChangeCallback
}

// DefaultClientConfig returns sensible defaults.
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		StreamerAddr:      "localhost:50052",
		InitialBackoff:    1 * time.Second,
		MaxBackoff:        30 * time.Second,
		BackoffMultiplier: 2.0,
		MaxRetries:        0, // unlimited
		HeartbeatInterval: 30 * time.Second,
		ActivityTimeout:   90 * time.Second,
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *ClientConfig) ApplyDefaults() {
	defaults := DefaultClientConfig()
	if c.StreamerAddr == "" {
		c.StreamerAddr = defaults.StreamerAddr
	}
	if c.InitialBackoff == 0 {
		c.InitialBackoff = defaults.InitialBackoff
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = defaults.MaxBackoff
	}
	if c.BackoffMultiplier == 0 {
		c.BackoffMultiplier = defaults.BackoffMultiplier
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = defaults.HeartbeatInterval
	}
	if c.ActivityTimeout == 0 {
		c.ActivityTimeout = defaults.ActivityTimeout
	}
}

// streamerClient implements the Service interface for remote Streamer.
type streamerClient struct {
	config ClientConfig
	logger *slog.Logger

	conn   *grpc.ClientConn
	client pb.StreamerServiceClient

	ctx    context.Context
	cancel context.CancelFunc
}

// NewClient creates a new streamerClient and returns the Service interface.
func NewClient(config ClientConfig, logger *slog.Logger) (Service, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Apply defaults
	if config.InitialBackoff <= 0 {
		config.InitialBackoff = 1 * time.Second
	}
	if config.MaxBackoff <= 0 {
		config.MaxBackoff = 30 * time.Second
	}
	if config.BackoffMultiplier <= 0 {
		config.BackoffMultiplier = 2.0
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 30 * time.Second
	}
	if config.ActivityTimeout <= 0 {
		config.ActivityTimeout = 90 * time.Second
	}

	ctx, cancel := context.WithCancel(context.Background())

	c := &streamerClient{
		config: config,
		logger: logger.With("component", "streamer-client"),
		ctx:    ctx,
		cancel: cancel,
	}

	// Connect to the remote Streamer
	conn, err := grpc.NewClient(config.StreamerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to connect to streamer: %w", err)
	}

	c.conn = conn
	c.client = pb.NewStreamerServiceClient(conn)

	c.logger.Info("Connected to Streamer", "addr", config.StreamerAddr)
	return c, nil
}

// Stream implements the Service interface.
// Returns a bidirectional stream that wraps the gRPC stream with auto-reconnect.
func (c *streamerClient) Stream(ctx context.Context) (Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(ctx)
	stopClient := context.AfterFunc(c.ctx, cancel)
	attemptCtx, attemptCancel := context.WithCancel(lifetime)
	grpcStream, err := c.client.Stream(attemptCtx)
	if err != nil {
		attemptCancel()
		cancel()
		stopClient()
		return nil, fmt.Errorf("failed to establish stream: %w", err)
	}
	if err := lifetime.Err(); err != nil {
		attemptCancel()
		cancel()
		stopClient()
		return nil, err
	}
	if err := c.ctx.Err(); err != nil {
		attemptCancel()
		cancel()
		stopClient()
		return nil, err
	}
	return newRemoteStream(lifetime, cancel, c, grpcStream, attemptCtx, attemptCancel, stopClient), nil
}

// Close closes the client connection.
func (c *streamerClient) Close() error {
	c.cancel()
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// Compile-time check
var _ Service = (*streamerClient)(nil)
