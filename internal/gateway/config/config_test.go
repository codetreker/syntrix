package config

import (
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	services "github.com/syntrixbase/syntrix/internal/services/config"
)

func TestReplicaConfigDefaultsAndValidation(t *testing.T) {
	defaults := DefaultReplicaConfig()
	assert.Equal(t, 1024, defaults.Connections)
	assert.Equal(t, 4096, defaults.Subscriptions)
	assert.Equal(t, 256, defaults.SubscriptionsPerConnection)
	assert.Equal(t, 256, defaults.PendingRegistrations)
	assert.Equal(t, int64(64<<20), defaults.SourceBytes)
	assert.Equal(t, int64(4<<20), defaults.SourceBytesPerConnection)
	assert.Equal(t, 8, defaults.ReadConcurrency)
	assert.Equal(t, int64(256<<20), defaults.PageBytes)
	assert.Equal(t, 4, defaults.PageCreditsPerConnection)
	assert.Equal(t, 8, defaults.AuthConcurrency)
	assert.Equal(t, 100, defaults.AuthRate)
	assert.Equal(t, 100, defaults.AuthBurst)
	assert.Equal(t, defaults, DefaultGatewayConfig().Realtime.Replica)
	cfg := GatewayConfig{}
	cfg.ApplyDefaults()
	assert.Equal(t, defaults, cfg.Realtime.Replica)
	assert.NoError(t, cfg.Validate(services.ModeStandalone))
	for i := 0; i < reflect.TypeOf(defaults).NumField(); i++ {
		field := reflect.TypeOf(defaults).Field(i)
		t.Run(field.Name, func(t *testing.T) {
			custom := ReplicaConfig{}
			reflect.ValueOf(&custom).Elem().Field(i).SetInt(3)
			custom.ApplyDefaults()
			assert.EqualValues(t, 3, reflect.ValueOf(custom).Field(i).Int())
			assert.NoError(t, custom.Validate())
			reflect.ValueOf(&custom).Elem().Field(i).SetInt(-1)
			custom.ApplyDefaults()
			assert.EqualValues(t, -1, reflect.ValueOf(custom).Field(i).Int())
			cfg := DefaultGatewayConfig()
			cfg.Realtime.Replica = custom
			assert.ErrorContains(t, cfg.Validate(services.ModeStandalone), "gateway.realtime.replica."+field.Tag.Get("yaml"))
		})
	}
}

func TestDefaultGatewayConfig(t *testing.T) {
	cfg := DefaultGatewayConfig()

	assert.Equal(t, "localhost:9000", cfg.QueryServiceURL)
	assert.Equal(t, "localhost:9000", cfg.StreamerServiceURL)
	assert.Equal(t, []string{"http://localhost:8080", "http://localhost:3000", "http://localhost:5173"}, cfg.Realtime.AllowedOrigins)
	assert.True(t, cfg.Realtime.AllowDevOrigin)
}

func TestGatewayConfig_StructFields(t *testing.T) {
	cfg := GatewayConfig{
		QueryServiceURL: "http://custom:9090",
		Realtime: RealtimeConfig{
			AllowedOrigins: []string{"https://example.com"},
			AllowDevOrigin: false,
		},
	}

	assert.Equal(t, "http://custom:9090", cfg.QueryServiceURL)
	assert.Equal(t, []string{"https://example.com"}, cfg.Realtime.AllowedOrigins)
	assert.False(t, cfg.Realtime.AllowDevOrigin)
}

func TestGatewayConfig_Validate(t *testing.T) {
	cfg := DefaultGatewayConfig()
	err := cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}

func TestGatewayConfig_ApplyDefaults(t *testing.T) {
	cfg := &GatewayConfig{}
	cfg.ApplyDefaults()

	assert.Equal(t, "localhost:9000", cfg.QueryServiceURL)
	assert.Equal(t, "localhost:9000", cfg.StreamerServiceURL)
	assert.Len(t, cfg.Realtime.AllowedOrigins, 3)
}

func TestGatewayConfig_ApplyEnvOverrides(t *testing.T) {
	os.Setenv("GATEWAY_QUERY_SERVICE_URL", "http://env:9090")
	defer os.Unsetenv("GATEWAY_QUERY_SERVICE_URL")

	cfg := DefaultGatewayConfig()
	cfg.ApplyEnvOverrides()

	assert.Equal(t, "http://env:9090", cfg.QueryServiceURL)
}

func TestGatewayConfig_ResolvePaths(t *testing.T) {
	cfg := DefaultGatewayConfig()
	cfg.ResolvePaths("config", "data")
	// No paths to resolve, just verify no panic
}

func TestGatewayConfig_ApplyDefaults_CustomValuesPreserved(t *testing.T) {
	cfg := &GatewayConfig{
		QueryServiceURL:    "custom:9001",
		StreamerServiceURL: "custom:9002",
		Realtime: RealtimeConfig{
			AllowedOrigins: []string{"https://prod.example.com"},
			AllowDevOrigin: false,
		},
	}
	cfg.ApplyDefaults()

	assert.Equal(t, "custom:9001", cfg.QueryServiceURL)
	assert.Equal(t, "custom:9002", cfg.StreamerServiceURL)
	assert.Equal(t, []string{"https://prod.example.com"}, cfg.Realtime.AllowedOrigins)
	assert.False(t, cfg.Realtime.AllowDevOrigin)
}

func TestGatewayConfig_ApplyDefaults_PartialConfig(t *testing.T) {
	cfg := &GatewayConfig{
		QueryServiceURL: "partial:9001",
		// StreamerServiceURL empty, should get default
		// Realtime.AllowedOrigins empty, should get defaults
	}
	cfg.ApplyDefaults()

	assert.Equal(t, "partial:9001", cfg.QueryServiceURL)
	assert.Equal(t, "localhost:9000", cfg.StreamerServiceURL)
	assert.Len(t, cfg.Realtime.AllowedOrigins, 3)
}

func TestGatewayConfig_ApplyEnvOverrides_WithTSetenv(t *testing.T) {
	t.Setenv("GATEWAY_QUERY_SERVICE_URL", "http://testenv:9999")

	cfg := DefaultGatewayConfig()
	cfg.ApplyEnvOverrides()

	assert.Equal(t, "http://testenv:9999", cfg.QueryServiceURL)
}

func TestGatewayConfig_ApplyEnvOverrides_NoEnvVar(t *testing.T) {
	cfg := DefaultGatewayConfig()
	originalURL := cfg.QueryServiceURL

	cfg.ApplyEnvOverrides()

	assert.Equal(t, originalURL, cfg.QueryServiceURL)
}

func TestGatewayConfig_Validate_EmptyConfig(t *testing.T) {
	cfg := GatewayConfig{}
	err := cfg.Validate(services.ModeStandalone)
	assert.NoError(t, err)
}

func TestGatewayConfig_Validate_DistributedMode(t *testing.T) {
	// In distributed mode, both QueryServiceURL and StreamerServiceURL are required
	cfg := &GatewayConfig{QueryServiceURL: "", StreamerServiceURL: "streamer:9000"}
	err := cfg.Validate(services.ModeDistributed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "gateway.query_service_url is required in distributed mode")

	// With QueryServiceURL but no StreamerServiceURL
	cfg.QueryServiceURL = "query:9000"
	cfg.StreamerServiceURL = ""
	err = cfg.Validate(services.ModeDistributed)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "gateway.streamer_service_url is required in distributed mode")

	// With both set, should pass
	cfg.StreamerServiceURL = "streamer:9000"
	err = cfg.Validate(services.ModeDistributed)
	assert.NoError(t, err)
}
