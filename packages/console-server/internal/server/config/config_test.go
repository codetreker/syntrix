package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConfigOriginAndTimeoutRequirements(t *testing.T) {
	for _, origin := range []string{"http://localhost:8090", "http://127.0.0.1:8090", "http://[::1]:8090", "https://console.example"} {
		t.Run(origin, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.PublicOrigin = origin
			assert.NoError(t, cfg.Validate())
		})
	}
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{"public plaintext origin", func(c *Config) { c.PublicOrigin = "http://console.example" }},
		{"plaintext localhost suffix", func(c *Config) { c.PublicOrigin = "http://localhost.example" }},
		{"empty origin", func(c *Config) { c.PublicOrigin = "" }},
		{"invalid escape", func(c *Config) { c.PublicOrigin = "https://%console" }},
		{"credentials in origin", func(c *Config) { c.PublicOrigin = "https://user:password@console.example" }},
		{"origin path", func(c *Config) { c.PublicOrigin = "https://console.example/console" }},
		{"origin query", func(c *Config) { c.PublicOrigin = "https://console.example?secret=value" }},
		{"origin fragment", func(c *Config) { c.PublicOrigin = "https://console.example#fragment" }},
		{"unsupported scheme", func(c *Config) { c.PublicOrigin = "ftp://console.example" }},
		{"missing address", func(c *Config) { c.Address = "" }},
		{"read timeout", func(c *Config) { c.ReadTimeout = 0 }},
		{"write timeout", func(c *Config) { c.WriteTimeout = -time.Second }},
		{"zero request timeout", func(c *Config) { c.RequestTimeout = 0 }},
		{"request timeout exceeds write timeout", func(c *Config) { c.RequestTimeout = c.WriteTimeout + time.Second }},
		{"request timeout equals write timeout", func(c *Config) { c.RequestTimeout = c.WriteTimeout }},
		{"shutdown timeout", func(c *Config) { c.ShutdownTimeout = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			test.change(&cfg)
			assert.Error(t, cfg.Validate())
		})
	}
}
