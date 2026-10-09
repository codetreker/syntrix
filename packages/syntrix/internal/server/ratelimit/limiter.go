// Package ratelimit provides rate limiting functionality for API endpoints.
package ratelimit

import (
	"time"
)

// Limiter defines the interface for rate limiting implementations.
type Limiter interface {
	// Allow checks if a request from the given key should be allowed.
	// Returns true if the request is allowed, false if it should be rate limited.
	Allow(key string) bool

	// Reset clears the rate limit counter for the given key.
	Reset(key string)
}

// Config holds the configuration for rate limiting.
type Config struct {
	// Enabled controls whether rate limiting is active.
	Enabled bool `yaml:"enabled"`

	// Requests is the maximum number of requests allowed per window.
	Requests int `yaml:"requests"`

	// Window is the duration of the rate limiting window.
	Window time.Duration `yaml:"window"`
}

// DefaultConfig returns the default rate limiting configuration.
func DefaultConfig() Config {
	return Config{
		Enabled:  true,
		Requests: 100,
		Window:   time.Minute,
	}
}

// AuthConfig returns a stricter rate limit configuration for authentication endpoints.
func AuthConfig() Config {
	return Config{
		Enabled:  true,
		Requests: 5,
		Window:   time.Minute,
	}
}
