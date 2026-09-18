package server

import (
	"time"

	services "github.com/syntrixbase/syntrix/internal/services/config"
)

// Config holds the configuration for the unified server module.
type Config struct {
	Host string `yaml:"host"`

	// HTTP Configuration
	HTTPPort         int           `yaml:"http_port"`
	HTTPReadTimeout  time.Duration `yaml:"http_read_timeout"`
	HTTPWriteTimeout time.Duration `yaml:"http_write_timeout"`
	HTTPIdleTimeout  time.Duration `yaml:"http_idle_timeout"`
	EnableCORS       bool          `yaml:"enable_cors"`

	// CORS Configuration
	AllowedOrigins   []string `yaml:"allowed_origins"`   // Allowed origins for CORS (empty allows all when EnableCORS is true)
	AllowedMethods   []string `yaml:"allowed_methods"`   // Allowed HTTP methods for CORS
	AllowedHeaders   []string `yaml:"allowed_headers"`   // Allowed headers for CORS
	AllowCredentials bool     `yaml:"allow_credentials"` // Allow credentials in CORS requests
	CORSMaxAge       int      `yaml:"cors_max_age"`      // Max age for CORS preflight cache in seconds

	// Rate Limiting Configuration
	RateLimit RateLimitConfig `yaml:"rate_limit"`

	// gRPC Configuration
	GRPCPort          int  `yaml:"grpc_port"`
	GRPCMaxConcurrent uint `yaml:"grpc_max_concurrent"`
	EnableReflection  bool `yaml:"enable_reflection"`

	// Lifecycle Configuration
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

// RateLimitConfig holds rate limiting configuration.
type RateLimitConfig struct {
	Enabled      bool          `yaml:"enabled"`       // Enable rate limiting
	Requests     int           `yaml:"requests"`      // Max requests per window (general endpoints)
	Window       time.Duration `yaml:"window"`        // Time window for rate limiting
	AuthRequests int           `yaml:"auth_requests"` // Max requests per window (auth endpoints)
	AuthWindow   time.Duration `yaml:"auth_window"`   // Time window for auth rate limiting
}

// DefaultConfig returns safe defaults for development.
func DefaultConfig() Config {
	return Config{
		Host:             "localhost",
		HTTPPort:         8080,
		HTTPReadTimeout:  10 * time.Second,
		HTTPWriteTimeout: 10 * time.Second,
		HTTPIdleTimeout:  60 * time.Second,
		GRPCPort:         9000,
		ShutdownTimeout:  10 * time.Second,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Request-ID", "X-Syntrix-Expected-Database-Identity"},
		AllowCredentials: true,
		CORSMaxAge:       86400, // 24 hours
		RateLimit: RateLimitConfig{
			Enabled:      true,
			Requests:     100,
			Window:       time.Minute,
			AuthRequests: 5,
			AuthWindow:   time.Minute,
		},
	}
}

// ApplyDefaults fills in zero values with defaults.
func (c *Config) ApplyDefaults() {
	defaults := DefaultConfig()
	if c.Host == "" {
		c.Host = defaults.Host
	}
	if c.HTTPPort == 0 {
		c.HTTPPort = defaults.HTTPPort
	}
	if c.HTTPReadTimeout == 0 {
		c.HTTPReadTimeout = defaults.HTTPReadTimeout
	}
	if c.HTTPWriteTimeout == 0 {
		c.HTTPWriteTimeout = defaults.HTTPWriteTimeout
	}
	if c.HTTPIdleTimeout == 0 {
		c.HTTPIdleTimeout = defaults.HTTPIdleTimeout
	}
	if c.GRPCPort == 0 {
		c.GRPCPort = defaults.GRPCPort
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = defaults.ShutdownTimeout
	}
	if len(c.AllowedMethods) == 0 {
		c.AllowedMethods = defaults.AllowedMethods
	}
	if len(c.AllowedHeaders) == 0 {
		c.AllowedHeaders = defaults.AllowedHeaders
	}
	// Note: AllowCredentials defaults to false (zero value), which is secure
	// Only set default if explicitly configured to true in DefaultConfig
	if c.CORSMaxAge == 0 {
		c.CORSMaxAge = defaults.CORSMaxAge
	}
	// Apply rate limit defaults
	if c.RateLimit.Requests == 0 {
		c.RateLimit.Requests = defaults.RateLimit.Requests
	}
	if c.RateLimit.Window == 0 {
		c.RateLimit.Window = defaults.RateLimit.Window
	}
	if c.RateLimit.AuthRequests == 0 {
		c.RateLimit.AuthRequests = defaults.RateLimit.AuthRequests
	}
	if c.RateLimit.AuthWindow == 0 {
		c.RateLimit.AuthWindow = defaults.RateLimit.AuthWindow
	}
}

// ApplyEnvOverrides applies environment variable overrides.
// No env vars for server config currently.
func (c *Config) ApplyEnvOverrides() { _ = c }

// ResolvePaths resolves relative paths using the given directories.
// No paths to resolve in server config.
func (c *Config) ResolvePaths(_, _ string) { _ = c }

// Validate returns an error if the configuration is invalid.
func (c *Config) Validate(_ services.DeploymentMode) error {
	return nil
}
