package config

import (
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/origin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadinessConfigComposition(t *testing.T) {
	t.Setenv("SYNTRIX_SERVICES_READINESS_INTERVAL", "99h")
	t.Setenv("SYNTRIX_SERVICES_READINESS_TIMEOUT", "99h")

	tests := []struct {
		name  string
		input ReadinessConfig
		want  ReadinessConfig
	}{
		{"defaults", ReadinessConfig{}, DefaultConfig().Readiness},
		{"default interval", ReadinessConfig{Timeout: 4 * time.Second}, ReadinessConfig{Interval: time.Second, Timeout: 4 * time.Second}},
		{"default timeout", ReadinessConfig{Interval: 3 * time.Second}, ReadinessConfig{Interval: 3 * time.Second, Timeout: 2 * time.Second}},
		{"explicit settings", ReadinessConfig{Interval: 5 * time.Second, Timeout: 7 * time.Second}, ReadinessConfig{Interval: 5 * time.Second, Timeout: 7 * time.Second}},
		{"invalid settings remain invalid", ReadinessConfig{Interval: -time.Second, Timeout: -time.Second}, ReadinessConfig{Interval: -time.Second, Timeout: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{Readiness: tt.input}
			cfg.ApplyDefaults()
			cfg.ApplyEnvOverrides()
			cfg.ResolvePaths("config", "data")
			assert.Equal(t, tt.want, cfg.Readiness)
		})
	}
	assert.Equal(t, ReadinessConfig{Interval: time.Second, Timeout: 2 * time.Second}, DefaultConfig().Readiness)
}

func TestReadinessConfigBounds(t *testing.T) {
	tests := []struct {
		name       string
		config     ReadinessConfig
		errorField string
	}{
		{"defaults", DefaultConfig().Readiness, ""},
		{"small positive durations", ReadinessConfig{Interval: time.Nanosecond, Timeout: time.Nanosecond}, ""},
		{"maximum durations", ReadinessConfig{Interval: time.Minute, Timeout: time.Minute}, ""},
		{"zero interval", ReadinessConfig{Timeout: time.Second}, "services.readiness.interval"},
		{"negative interval", ReadinessConfig{Interval: -time.Nanosecond, Timeout: time.Second}, "services.readiness.interval"},
		{"interval exceeds limit", ReadinessConfig{Interval: time.Minute + time.Nanosecond, Timeout: time.Second}, "services.readiness.interval"},
		{"zero timeout", ReadinessConfig{Interval: time.Second}, "services.readiness.timeout"},
		{"negative timeout", ReadinessConfig{Interval: time.Second, Timeout: -time.Nanosecond}, "services.readiness.timeout"},
		{"timeout exceeds limit", ReadinessConfig{Interval: time.Second, Timeout: time.Minute + time.Nanosecond}, "services.readiness.timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []DeploymentMode{ModeStandalone, ModeDistributed} {
				cfg := Config{Readiness: tt.config}
				err := cfg.Validate(mode)
				if tt.errorField == "" {
					require.NoError(t, err)
				} else {
					require.ErrorContains(t, err, tt.errorField)
				}
			}
		})
	}
}

func TestValidateOrigins(t *testing.T) {
	allowed := origin.Policy{AllowedOrigins: []string{"https://console.example.com", "https://app.example.com:8443"}}
	development := origin.Policy{AllowedOrigins: []string{"*"}, AllowDevelopment: true}
	tests := []struct {
		name           string
		managed        bool
		cors           bool
		httpPolicy     origin.Policy
		realtimePolicy origin.Policy
		errorText      string
	}{
		{"managed exact origins", true, true, allowed, allowed, ""},
		{"managed origins in different order", true, true, allowed, origin.Policy{AllowedOrigins: []string{"https://app.example.com:8443", "https://console.example.com"}}, ""},
		{"managed same origin only", true, true, origin.Policy{}, origin.Policy{}, ""},
		{"shared local defaults", true, true, origin.Policy{AllowedOrigins: origin.DefaultAllowedOrigins()}, origin.Policy{AllowedOrigins: origin.DefaultAllowedOrigins()}, ""},
		{"managed CORS disabled", true, false, allowed, allowed, "requires REST origin enforcement"},
		{"managed development policy", true, true, development, development, "cannot enable development allowances"},
		{"managed realtime development policy", true, true, allowed, development, "cannot enable development allowances"},
		{"managed wildcard", true, true, origin.Policy{AllowedOrigins: []string{"*"}}, allowed, "allowed origins must be"},
		{"managed differing exact origins", true, true, allowed, origin.Policy{AllowedOrigins: []string{"https://other.example.com"}}, "must use one policy"},
		{"operator exact origins", false, true, allowed, allowed, ""},
		{"operator explicit development", false, false, development, development, ""},
		{"operator wildcard without development", false, true, origin.Policy{AllowedOrigins: []string{"*"}}, allowed, "allowed origins must be"},
		{"operator differing origin lists", false, true, allowed, origin.Policy{}, "must use one policy"},
		{"operator differing development flags", false, true, allowed, origin.Policy{AllowedOrigins: allowed.AllowedOrigins, AllowDevelopment: true}, "must use one policy"},
		{"invalid HTTP origin", false, true, origin.Policy{AllowedOrigins: []string{"https://app.example.com/path"}}, allowed, "allowed origins must be"},
		{"invalid realtime origin", false, true, allowed, origin.Policy{AllowedOrigins: []string{"null"}}, "allowed origins must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpOrigins := append([]string(nil), tt.httpPolicy.AllowedOrigins...)
			realtimeOrigins := append([]string(nil), tt.realtimePolicy.AllowedOrigins...)
			err := ValidateOrigins(tt.managed, tt.cors, tt.httpPolicy, tt.realtimePolicy)
			if tt.errorText == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.errorText)
			}
			assert.Equal(t, httpOrigins, tt.httpPolicy.AllowedOrigins)
			assert.Equal(t, realtimeOrigins, tt.realtimePolicy.AllowedOrigins)
		})
	}
}
