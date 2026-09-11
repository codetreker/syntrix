package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	services "github.com/syntrixbase/syntrix/internal/services/config"
)

// Config holds configuration for the Puller service.
type Config struct {
	// gRPC Server configuration
	GRPC GRPCConfig `yaml:"grpc"`

	// Backend configurations - references storage.backends by name
	Backends []PullerBackendConfig `yaml:"backends"`

	// Event buffer configuration
	Buffer BufferConfig `yaml:"buffer"`

	// Consumer management configuration
	Consumer ConsumerConfig `yaml:"consumer"`

	// Event cleanup configuration
	Cleaner CleanerConfig `yaml:"cleaner"`

	// Bootstrap configuration
	Bootstrap BootstrapConfig `yaml:"bootstrap"`

	// Observability configuration
	Metrics MetricsConfig `yaml:"metrics"`
	Health  HealthConfig  `yaml:"health"`
}

// GRPCConfig holds gRPC server configuration.
// Note: Address is no longer used as Puller registers with the unified gRPC server.
type GRPCConfig struct {
	// MaxConnections limits active Subscribe RPCs per gRPC server, including catch-up.
	// Each RPC counts independently of its consumer ID or transport connection.
	MaxConnections int `yaml:"max_connections"`
	// ChannelSize is the size of the subscriber channel.
	// Defaults to 10000 if not set.
	ChannelSize int `yaml:"channel_size"`
	// HeartbeatInterval is the interval at which the server sends heartbeat
	// events to connected clients to keep connections alive.
	// A heartbeat is a PullerEvent with nil ChangeEvent.
	// Defaults to 30 seconds if not set. Set to 0 to disable.
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
}

// PullerBackendConfig references a storage backend and adds puller-specific settings.
type PullerBackendConfig struct {
	// Name references storage.backends[name]
	Name string `yaml:"name"`

	// Collections to pull (whitelist)
	Collections []string `yaml:"collections"`
}

// BufferConfig holds PebbleDB event buffer configuration.
type BufferConfig struct {
	// Path for PebbleDB storage (per-backend subdirs created automatically)
	Path string `yaml:"path"`

	// Maximum size of the buffer (e.g., "10GiB", "1TiB")
	MaxSize string `yaml:"max_size"`

	// BatchSize is the max number of events per batch.
	BatchSize int `yaml:"batch_size"`

	// BatchInterval is the max time to wait before flushing a batch.
	BatchInterval time.Duration `yaml:"batch_interval"`

	// QueueSize limits events awaiting durable completion, including the active batch.
	QueueSize int `yaml:"queue_size"`
}

// ConsumerConfig holds consumer management configuration.
type ConsumerConfig struct {
	// Number of events behind to trigger catch-up mode
	CatchUpThreshold int `yaml:"catch_up_threshold"`

	// Enable coalescing when consumer is catching up
	CoalesceOnCatchUp bool `yaml:"coalesce_on_catch_up"`
}

// CleanerConfig holds event cleanup configuration.
type CleanerConfig struct {
	// How often to run cleanup
	Interval time.Duration `yaml:"interval"`

	// How long to retain events
	Retention time.Duration `yaml:"retention"`
}

// BootstrapConfig holds first-run configuration.
type BootstrapConfig struct {
	// Bootstrap mode: "from_now" or "from_beginning"
	Mode string `yaml:"mode"`
}

// MetricsConfig holds Prometheus metrics configuration.
type MetricsConfig struct {
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

// HealthConfig holds health check endpoint configuration.
type HealthConfig struct {
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

// DefaultConfig returns sensible defaults for PullerConfig.
func DefaultConfig() Config {
	return Config{
		GRPC: GRPCConfig{
			MaxConnections:    100,
			HeartbeatInterval: 30 * time.Second,
		},
		Backends: []PullerBackendConfig{
			{
				Name:        "default_mongo",
				Collections: []string{"documents", "sys"},
			},
		},
		Buffer: BufferConfig{
			Path:          "data/puller/events",
			MaxSize:       "10GiB",
			BatchSize:     100,
			BatchInterval: 100 * time.Millisecond,
			QueueSize:     10000,
		},
		Consumer: ConsumerConfig{
			CatchUpThreshold:  100000,
			CoalesceOnCatchUp: true,
		},
		Cleaner: CleanerConfig{
			Interval:  1 * time.Minute,
			Retention: 1 * time.Hour,
		},
		Bootstrap: BootstrapConfig{
			Mode: "from_now",
		},
		Metrics: MetricsConfig{
			Port: 9090,
			Path: "/metrics",
		},
		Health: HealthConfig{
			Port: 8081,
			Path: "/health",
		},
	}
}

// Validate validates the PullerConfig.
func (c *Config) Validate(_ services.DeploymentMode) error {
	if c.GRPC.MaxConnections <= 0 {
		return errors.New("puller.grpc.max_connections must be positive")
	}

	if len(c.Backends) == 0 {
		return errors.New("puller.backends must have at least one backend")
	}

	for i, b := range c.Backends {
		if b.Name == "" {
			return fmt.Errorf("puller.backends[%d].name is required", i)
		}
		if len(b.Collections) == 0 {
			return fmt.Errorf("puller.backends[%d].collections must specify at least one collection", i)
		}
	}

	if c.Buffer.Path == "" {
		return errors.New("puller.buffer.path is required")
	}

	if c.Buffer.BatchSize <= 0 {
		return errors.New("puller.buffer.batch_size must be positive")
	}

	if c.Buffer.BatchInterval <= 0 {
		return errors.New("puller.buffer.batch_interval must be positive")
	}

	if c.Buffer.QueueSize <= 0 {
		return errors.New("puller.buffer.queue_size must be positive")
	}

	if c.Consumer.CatchUpThreshold <= 0 {
		return errors.New("puller.consumer.catch_up_threshold must be positive")
	}

	if c.Cleaner.Interval <= 0 {
		return errors.New("puller.cleaner.interval must be positive")
	}

	if c.Cleaner.Retention <= 0 {
		return errors.New("puller.cleaner.retention must be positive")
	}

	if c.Bootstrap.Mode != "from_now" && c.Bootstrap.Mode != "from_beginning" {
		return fmt.Errorf("puller.bootstrap.mode must be 'from_now' or 'from_beginning', got %q", c.Bootstrap.Mode)
	}

	return nil
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.GRPC.MaxConnections <= 0 {
		c.GRPC.MaxConnections = defaults.GRPC.MaxConnections
	}
	if c.GRPC.HeartbeatInterval == 0 {
		c.GRPC.HeartbeatInterval = defaults.GRPC.HeartbeatInterval
	}
	if len(c.Backends) == 0 {
		c.Backends = defaults.Backends
	}
	if c.Buffer.Path == "" {
		c.Buffer.Path = defaults.Buffer.Path
	}
	if c.Buffer.MaxSize == "" {
		c.Buffer.MaxSize = defaults.Buffer.MaxSize
	}
	if c.Buffer.BatchSize <= 0 {
		c.Buffer.BatchSize = defaults.Buffer.BatchSize
	}
	if c.Buffer.BatchInterval == 0 {
		c.Buffer.BatchInterval = defaults.Buffer.BatchInterval
	}
	if c.Buffer.QueueSize <= 0 {
		c.Buffer.QueueSize = defaults.Buffer.QueueSize
	}
	if c.Consumer.CatchUpThreshold <= 0 {
		c.Consumer.CatchUpThreshold = defaults.Consumer.CatchUpThreshold
	}
	if c.Cleaner.Interval == 0 {
		c.Cleaner.Interval = defaults.Cleaner.Interval
	}
	if c.Cleaner.Retention == 0 {
		c.Cleaner.Retention = defaults.Cleaner.Retention
	}
	if c.Bootstrap.Mode == "" {
		c.Bootstrap.Mode = defaults.Bootstrap.Mode
	}
	if c.Metrics.Port == 0 {
		c.Metrics.Port = defaults.Metrics.Port
	}
	if c.Metrics.Path == "" {
		c.Metrics.Path = defaults.Metrics.Path
	}
	if c.Health.Port == 0 {
		c.Health.Port = defaults.Health.Port
	}
	if c.Health.Path == "" {
		c.Health.Path = defaults.Health.Path
	}
}

// ApplyEnvOverrides applies environment variable overrides.
// No env vars for puller config currently.
func (c *Config) ApplyEnvOverrides() { _ = c }

// ResolvePaths resolves relative paths using the given directories.
// - configDir: not used for puller config (no config-related paths)
// - dataDir: base directory for runtime data paths (buffer.path)
func (c *Config) ResolvePaths(configDir, dataDir string) {
	_ = configDir // puller has no config-related paths
	if c.Buffer.Path != "" && !filepath.IsAbs(c.Buffer.Path) {
		c.Buffer.Path = filepath.Join(dataDir, c.Buffer.Path)
	}
}

// ParseByteSize parses a human-readable byte size string (e.g., "10GiB", "1TiB").
// Returns the size in bytes.
func ParseByteSize(s string) (int64, error) {
	if s == "" {
		return 0, errors.New("empty size string")
	}

	// Find where the number ends and the unit begins
	var numEnd int
	for i, c := range s {
		if c < '0' || c > '9' {
			numEnd = i
			break
		}
		numEnd = i + 1
	}

	if numEnd == 0 {
		return 0, fmt.Errorf("invalid size string: %q", s)
	}

	numStr := s[:numEnd]
	unit := s[numEnd:]

	var num int64
	for _, c := range numStr {
		num = num*10 + int64(c-'0')
	}

	multiplier := int64(1)
	switch unit {
	case "", "B":
		multiplier = 1
	case "KiB", "K":
		multiplier = 1024
	case "MiB", "M":
		multiplier = 1024 * 1024
	case "GiB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TiB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit: %q", unit)
	}

	return num * multiplier, nil
}
