package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	services "github.com/syntrixbase/syntrix/internal/services/config"
)

func TestDefaultPullerConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.GRPC.MaxConnections != 100 {
		t.Errorf("GRPC.MaxConnections = %d, want %d", cfg.GRPC.MaxConnections, 100)
	}

	require.Equal(t, []PullerBackendConfig{{Name: "default_mongo", Collections: []string{"documents", "sys"}}}, cfg.Backends)

	if cfg.Consumer.CatchUpThreshold != 100000 {
		t.Errorf("Consumer.CatchUpThreshold = %d, want %d", cfg.Consumer.CatchUpThreshold, 100000)
	}

	if cfg.Cleaner.Retention != time.Hour {
		t.Errorf("Cleaner.Retention = %v, want %v", cfg.Cleaner.Retention, time.Hour)
	}

	if cfg.Buffer.BatchSize != 100 {
		t.Errorf("Buffer.BatchSize = %d, want %d", cfg.Buffer.BatchSize, 100)
	}

	if cfg.Buffer.BatchInterval != 100*time.Millisecond {
		t.Errorf("Buffer.BatchInterval = %v, want %v", cfg.Buffer.BatchInterval, 100*time.Millisecond)
	}

	if cfg.Buffer.QueueSize != 10000 {
		t.Errorf("Buffer.QueueSize = %d, want %d", cfg.Buffer.QueueSize, 10000)
	}

	if cfg.Bootstrap.Mode != "from_now" {
		t.Errorf("Bootstrap.Mode = %q, want %q", cfg.Bootstrap.Mode, "from_now")
	}

	// Default config should be valid
	if err := cfg.Validate(services.ModeDistributed); err != nil {
		t.Errorf("DefaultPullerConfig().Validate() error = %v", err)
	}
}

func TestPullerConfig_Validate(t *testing.T) {
	validCfg := func() Config {
		return DefaultConfig()
	}

	tests := []struct {
		name    string
		modify  func(*Config)
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid default config",
			modify:  func(c *Config) {},
			wantErr: false,
		},
		{
			name:    "zero max connections",
			modify:  func(c *Config) { c.GRPC.MaxConnections = 0 },
			wantErr: true,
			errMsg:  "max_connections",
		},
		{
			name:    "negative max connections",
			modify:  func(c *Config) { c.GRPC.MaxConnections = -1 },
			wantErr: true,
			errMsg:  "max_connections",
		},
		{
			name:    "empty backends",
			modify:  func(c *Config) { c.Backends = nil },
			wantErr: true,
			errMsg:  "backends",
		},
		{
			name: "backend with empty name",
			modify: func(c *Config) {
				c.Backends = []PullerBackendConfig{{Name: ""}}
			},
			wantErr: true,
			errMsg:  "name is required",
		},
		{
			name: "backend missing collections",
			modify: func(c *Config) {
				c.Backends = []PullerBackendConfig{{
					Name:        "test",
					Collections: nil,
				}}
			},
			wantErr: true,
			errMsg:  "collections must specify",
		},
		{
			name:    "empty buffer path",
			modify:  func(c *Config) { c.Buffer.Path = "" },
			wantErr: true,
			errMsg:  "path",
		},
		{
			name:    "zero buffer batch size",
			modify:  func(c *Config) { c.Buffer.BatchSize = 0 },
			wantErr: true,
			errMsg:  "batch_size",
		},
		{
			name:    "zero buffer batch interval",
			modify:  func(c *Config) { c.Buffer.BatchInterval = 0 },
			wantErr: true,
			errMsg:  "batch_interval",
		},
		{
			name:    "zero buffer queue size",
			modify:  func(c *Config) { c.Buffer.QueueSize = 0 },
			wantErr: true,
			errMsg:  "queue_size",
		},
		{
			name:    "zero catch up threshold",
			modify:  func(c *Config) { c.Consumer.CatchUpThreshold = 0 },
			wantErr: true,
			errMsg:  "catch_up_threshold",
		},
		{
			name:    "zero cleaner interval",
			modify:  func(c *Config) { c.Cleaner.Interval = 0 },
			wantErr: true,
			errMsg:  "interval",
		},
		{
			name:    "zero retention",
			modify:  func(c *Config) { c.Cleaner.Retention = 0 },
			wantErr: true,
			errMsg:  "retention",
		},
		{
			name:    "invalid bootstrap mode",
			modify:  func(c *Config) { c.Bootstrap.Mode = "invalid" },
			wantErr: true,
			errMsg:  "from_now' or 'from_beginning",
		},
		{
			name:    "from_beginning bootstrap mode is valid",
			modify:  func(c *Config) { c.Bootstrap.Mode = "from_beginning" },
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validCfg()
			tt.modify(&cfg)

			err := cfg.Validate(services.ModeDistributed)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && err != nil && tt.errMsg != "" {
				if !containsStr(err.Error(), tt.errMsg) {
					t.Errorf("Validate() error = %q, should contain %q", err.Error(), tt.errMsg)
				}
			}
		})
	}
}

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"1", 1, false},
		{"100", 100, false},
		{"1B", 1, false},
		{"1KiB", 1024, false},
		{"1K", 1024, false},
		{"1MiB", 1024 * 1024, false},
		{"1M", 1024 * 1024, false},
		{"1GiB", 1024 * 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"10GiB", 10 * 1024 * 1024 * 1024, false},
		{"1TiB", 1024 * 1024 * 1024 * 1024, false},
		{"1T", 1024 * 1024 * 1024 * 1024, false},
		{"", 0, true},
		{"GiB", 0, true},  // no number
		{"1XiB", 0, true}, // unknown unit
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseByteSize(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseByteSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ParseByteSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name     string
		initial  Config
		expected Config
	}{
		{
			name:    "empty config gets all defaults",
			initial: Config{},
			expected: Config{
				GRPC: GRPCConfig{
					MaxConnections:    100,
					HeartbeatInterval: 30 * time.Second,
				},
				Backends: []PullerBackendConfig{
					{Name: "default_mongo", Collections: []string{"documents", "sys"}},
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
					CoalesceOnCatchUp: false,
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
			},
		},
		{
			name: "custom values preserved",
			initial: Config{
				GRPC: GRPCConfig{
					MaxConnections:    200,
					HeartbeatInterval: 60 * time.Second,
				},
				Backends: []PullerBackendConfig{
					{Name: "custom_backend", Collections: []string{"users"}},
				},
				Buffer: BufferConfig{
					Path:          "/custom/path",
					MaxSize:       "5GiB",
					BatchSize:     50,
					BatchInterval: 50 * time.Millisecond,
					QueueSize:     5000,
				},
				Consumer: ConsumerConfig{
					CatchUpThreshold:  50000,
					CoalesceOnCatchUp: true,
				},
				Cleaner: CleanerConfig{
					Interval:  2 * time.Minute,
					Retention: 2 * time.Hour,
				},
				Bootstrap: BootstrapConfig{
					Mode: "from_beginning",
				},
				Metrics: MetricsConfig{
					Port: 9091,
					Path: "/custom/metrics",
				},
				Health: HealthConfig{
					Port: 8082,
					Path: "/custom/health",
				},
			},
			expected: Config{
				GRPC: GRPCConfig{
					MaxConnections:    200,
					HeartbeatInterval: 60 * time.Second,
				},
				Backends: []PullerBackendConfig{
					{Name: "custom_backend", Collections: []string{"users"}},
				},
				Buffer: BufferConfig{
					Path:          "/custom/path",
					MaxSize:       "5GiB",
					BatchSize:     50,
					BatchInterval: 50 * time.Millisecond,
					QueueSize:     5000,
				},
				Consumer: ConsumerConfig{
					CatchUpThreshold:  50000,
					CoalesceOnCatchUp: true,
				},
				Cleaner: CleanerConfig{
					Interval:  2 * time.Minute,
					Retention: 2 * time.Hour,
				},
				Bootstrap: BootstrapConfig{
					Mode: "from_beginning",
				},
				Metrics: MetricsConfig{
					Port: 9091,
					Path: "/custom/metrics",
				},
				Health: HealthConfig{
					Port: 8082,
					Path: "/custom/health",
				},
			},
		},
		{
			name: "partial config gets remaining defaults",
			initial: Config{
				GRPC: GRPCConfig{
					MaxConnections: 150,
					// HeartbeatInterval is zero, should get default
				},
				Buffer: BufferConfig{
					Path: "/my/path",
					// Other fields zero, should get defaults
				},
				Metrics: MetricsConfig{
					Port: 9095,
					// Path empty, should get default
				},
			},
			expected: Config{
				GRPC: GRPCConfig{
					MaxConnections:    150,
					HeartbeatInterval: 30 * time.Second,
				},
				Backends: []PullerBackendConfig{
					{Name: "default_mongo", Collections: []string{"documents", "sys"}},
				},
				Buffer: BufferConfig{
					Path:          "/my/path",
					MaxSize:       "10GiB",
					BatchSize:     100,
					BatchInterval: 100 * time.Millisecond,
					QueueSize:     10000,
				},
				Consumer: ConsumerConfig{
					CatchUpThreshold: 100000,
				},
				Cleaner: CleanerConfig{
					Interval:  1 * time.Minute,
					Retention: 1 * time.Hour,
				},
				Bootstrap: BootstrapConfig{
					Mode: "from_now",
				},
				Metrics: MetricsConfig{
					Port: 9095,
					Path: "/metrics",
				},
				Health: HealthConfig{
					Port: 8081,
					Path: "/health",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.initial
			cfg.ApplyDefaults()

			if cfg.GRPC.MaxConnections != tt.expected.GRPC.MaxConnections {
				t.Errorf("GRPC.MaxConnections = %d, want %d", cfg.GRPC.MaxConnections, tt.expected.GRPC.MaxConnections)
			}
			if cfg.GRPC.HeartbeatInterval != tt.expected.GRPC.HeartbeatInterval {
				t.Errorf("GRPC.HeartbeatInterval = %v, want %v", cfg.GRPC.HeartbeatInterval, tt.expected.GRPC.HeartbeatInterval)
			}
			require.Equal(t, tt.expected.Backends, cfg.Backends)
			if cfg.Buffer.Path != tt.expected.Buffer.Path {
				t.Errorf("Buffer.Path = %q, want %q", cfg.Buffer.Path, tt.expected.Buffer.Path)
			}
			if cfg.Buffer.MaxSize != tt.expected.Buffer.MaxSize {
				t.Errorf("Buffer.MaxSize = %q, want %q", cfg.Buffer.MaxSize, tt.expected.Buffer.MaxSize)
			}
			if cfg.Buffer.BatchSize != tt.expected.Buffer.BatchSize {
				t.Errorf("Buffer.BatchSize = %d, want %d", cfg.Buffer.BatchSize, tt.expected.Buffer.BatchSize)
			}
			if cfg.Buffer.BatchInterval != tt.expected.Buffer.BatchInterval {
				t.Errorf("Buffer.BatchInterval = %v, want %v", cfg.Buffer.BatchInterval, tt.expected.Buffer.BatchInterval)
			}
			if cfg.Buffer.QueueSize != tt.expected.Buffer.QueueSize {
				t.Errorf("Buffer.QueueSize = %d, want %d", cfg.Buffer.QueueSize, tt.expected.Buffer.QueueSize)
			}
			if cfg.Consumer.CatchUpThreshold != tt.expected.Consumer.CatchUpThreshold {
				t.Errorf("Consumer.CatchUpThreshold = %d, want %d", cfg.Consumer.CatchUpThreshold, tt.expected.Consumer.CatchUpThreshold)
			}
			if cfg.Cleaner.Interval != tt.expected.Cleaner.Interval {
				t.Errorf("Cleaner.Interval = %v, want %v", cfg.Cleaner.Interval, tt.expected.Cleaner.Interval)
			}
			if cfg.Cleaner.Retention != tt.expected.Cleaner.Retention {
				t.Errorf("Cleaner.Retention = %v, want %v", cfg.Cleaner.Retention, tt.expected.Cleaner.Retention)
			}
			if cfg.Bootstrap.Mode != tt.expected.Bootstrap.Mode {
				t.Errorf("Bootstrap.Mode = %q, want %q", cfg.Bootstrap.Mode, tt.expected.Bootstrap.Mode)
			}
			if cfg.Metrics.Port != tt.expected.Metrics.Port {
				t.Errorf("Metrics.Port = %d, want %d", cfg.Metrics.Port, tt.expected.Metrics.Port)
			}
			if cfg.Metrics.Path != tt.expected.Metrics.Path {
				t.Errorf("Metrics.Path = %q, want %q", cfg.Metrics.Path, tt.expected.Metrics.Path)
			}
			if cfg.Health.Port != tt.expected.Health.Port {
				t.Errorf("Health.Port = %d, want %d", cfg.Health.Port, tt.expected.Health.Port)
			}
			if cfg.Health.Path != tt.expected.Health.Path {
				t.Errorf("Health.Path = %q, want %q", cfg.Health.Path, tt.expected.Health.Path)
			}
		})
	}
}

func TestConfig_ApplyEnvOverrides(t *testing.T) {
	// ApplyEnvOverrides is currently a no-op for puller config
	// Verify it doesn't panic and doesn't modify config
	cfg := DefaultConfig()
	originalPath := cfg.Buffer.Path

	cfg.ApplyEnvOverrides()

	if cfg.Buffer.Path != originalPath {
		t.Errorf("ApplyEnvOverrides modified Buffer.Path unexpectedly")
	}
}

func TestConfig_ResolvePaths(t *testing.T) {
	tests := []struct {
		name         string
		configDir    string
		dataDir      string
		bufferPath   string
		expectedPath string
	}{
		{
			name:         "relative path gets resolved with dataDir",
			configDir:    "/config",
			dataDir:      "/app/data",
			bufferPath:   "puller/events",
			expectedPath: "/app/data/puller/events",
		},
		{
			name:         "absolute path preserved",
			configDir:    "/config",
			dataDir:      "/app/data",
			bufferPath:   "/absolute/events",
			expectedPath: "/absolute/events",
		},
		{
			name:         "nested relative path",
			configDir:    "/config",
			dataDir:      "/var/lib/syntrix",
			bufferPath:   "puller/buffer/events",
			expectedPath: "/var/lib/syntrix/puller/buffer/events",
		},
		{
			name:         "empty path stays empty",
			configDir:    "/config",
			dataDir:      "/app/data",
			bufferPath:   "",
			expectedPath: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				Buffer: BufferConfig{
					Path: tt.bufferPath,
				},
			}
			cfg.ResolvePaths(tt.configDir, tt.dataDir)
			if cfg.Buffer.Path != tt.expectedPath {
				t.Errorf("Buffer.Path = %q, want %q", cfg.Buffer.Path, tt.expectedPath)
			}
		})
	}
}
