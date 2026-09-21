package config

import (
	"fmt"
	"os"

	services "github.com/syntrixbase/syntrix/internal/services/config"
)

type GatewayConfig struct {
	QueryServiceURL    string         `yaml:"query_service_url"`
	StreamerServiceURL string         `yaml:"streamer_service_url"`
	Realtime           RealtimeConfig `yaml:"realtime"`
}

type RealtimeConfig struct {
	AllowedOrigins []string      `yaml:"allowed_origins"`
	AllowDevOrigin bool          `yaml:"allow_dev_origin"`
	Replica        ReplicaConfig `yaml:"replica"`
}

type ReplicaConfig struct {
	Connections                int   `yaml:"connections"`
	Subscriptions              int   `yaml:"subscriptions"`
	SubscriptionsPerConnection int   `yaml:"subscriptions_per_connection"`
	PendingRegistrations       int   `yaml:"pending_registrations"`
	SourceBytes                int64 `yaml:"source_bytes"`
	SourceBytesPerConnection   int64 `yaml:"source_bytes_per_connection"`
	ReadConcurrency            int   `yaml:"read_concurrency"`
	PageBytes                  int64 `yaml:"page_bytes"`
	PageCreditsPerConnection   int   `yaml:"page_credits_per_connection"`
	AuthConcurrency            int   `yaml:"auth_concurrency"`
	AuthRate                   int   `yaml:"auth_rate"`
	AuthBurst                  int   `yaml:"auth_burst"`
}

func DefaultReplicaConfig() ReplicaConfig {
	return ReplicaConfig{
		Connections: 1024, Subscriptions: 4096, SubscriptionsPerConnection: 256,
		PendingRegistrations: 256, SourceBytes: 64 << 20, SourceBytesPerConnection: 4 << 20,
		ReadConcurrency: 8, PageBytes: 256 << 20, PageCreditsPerConnection: 4,
		AuthConcurrency: 8, AuthRate: 100, AuthBurst: 100,
	}
}

func (r *ReplicaConfig) ApplyDefaults() {
	d := DefaultReplicaConfig()
	for _, field := range []struct {
		value    *int
		fallback int
	}{
		{&r.Connections, d.Connections}, {&r.Subscriptions, d.Subscriptions},
		{&r.SubscriptionsPerConnection, d.SubscriptionsPerConnection}, {&r.PendingRegistrations, d.PendingRegistrations},
		{&r.ReadConcurrency, d.ReadConcurrency}, {&r.PageCreditsPerConnection, d.PageCreditsPerConnection},
		{&r.AuthConcurrency, d.AuthConcurrency}, {&r.AuthRate, d.AuthRate}, {&r.AuthBurst, d.AuthBurst},
	} {
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
	for _, field := range []struct {
		value    *int64
		fallback int64
	}{
		{&r.SourceBytes, d.SourceBytes}, {&r.SourceBytesPerConnection, d.SourceBytesPerConnection}, {&r.PageBytes, d.PageBytes},
	} {
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
}

func (r ReplicaConfig) Validate() error {
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"connections", int64(r.Connections)}, {"subscriptions", int64(r.Subscriptions)},
		{"subscriptions_per_connection", int64(r.SubscriptionsPerConnection)}, {"pending_registrations", int64(r.PendingRegistrations)},
		{"source_bytes", r.SourceBytes}, {"source_bytes_per_connection", r.SourceBytesPerConnection},
		{"read_concurrency", int64(r.ReadConcurrency)}, {"page_bytes", r.PageBytes},
		{"page_credits_per_connection", int64(r.PageCreditsPerConnection)}, {"auth_concurrency", int64(r.AuthConcurrency)},
		{"auth_rate", int64(r.AuthRate)}, {"auth_burst", int64(r.AuthBurst)},
	} {
		if field.value < 0 {
			return fmt.Errorf("gateway.realtime.replica.%s must be positive or zero for its default", field.name)
		}
	}
	return nil
}

func DefaultGatewayConfig() GatewayConfig {
	return GatewayConfig{
		QueryServiceURL:    "localhost:9000",
		StreamerServiceURL: "localhost:9000",
		Realtime: RealtimeConfig{
			AllowedOrigins: []string{"http://localhost:8080", "http://localhost:3000", "http://localhost:5173"},
			AllowDevOrigin: true,
			Replica:        DefaultReplicaConfig(),
		},
	}
}

// ApplyDefaults fills in zero values with defaults.
func (g *GatewayConfig) ApplyDefaults() {
	defaults := DefaultGatewayConfig()
	if g.QueryServiceURL == "" {
		g.QueryServiceURL = defaults.QueryServiceURL
	}
	if g.StreamerServiceURL == "" {
		g.StreamerServiceURL = defaults.StreamerServiceURL
	}
	if len(g.Realtime.AllowedOrigins) == 0 {
		g.Realtime.AllowedOrigins = defaults.Realtime.AllowedOrigins
	}
	g.Realtime.Replica.ApplyDefaults()
}

// ApplyEnvOverrides applies environment variable overrides.
func (g *GatewayConfig) ApplyEnvOverrides() {
	if val := os.Getenv("GATEWAY_QUERY_SERVICE_URL"); val != "" {
		g.QueryServiceURL = val
	}
}

// ResolvePaths resolves relative paths using the given directories.
// No paths to resolve in gateway config.
func (g *GatewayConfig) ResolvePaths(_, _ string) { _ = g }

// Validate returns an error if the configuration is invalid.
func (g *GatewayConfig) Validate(mode services.DeploymentMode) error {
	if mode.IsDistributed() {
		if g.QueryServiceURL == "" {
			return fmt.Errorf("gateway.query_service_url is required in distributed mode")
		}
		if g.StreamerServiceURL == "" {
			return fmt.Errorf("gateway.streamer_service_url is required in distributed mode")
		}
	}
	return g.Realtime.Replica.Validate()
}
