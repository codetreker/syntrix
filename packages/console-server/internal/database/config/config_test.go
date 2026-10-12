package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConfigRequiresExplicitDatabaseAndUsablePool(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
		valid  bool
	}{
		{"valid defaults", func(*Config) {}, true},
		{"empty DSN", func(c *Config) { c.DSN = "" }, false},
		{"pool too small", func(c *Config) { c.MaxOpenConnections = 2 }, false},
		{"minimum pool", func(c *Config) { c.MaxOpenConnections = 3 }, true},
		{"zero timeout", func(c *Config) { c.ConnectTimeout = 0 }, false},
		{"negative timeout", func(c *Config) { c.ConnectTimeout = -time.Second }, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.DSN = "postgres://developer:password@localhost/console?sslmode=disable"
			test.change(&cfg)
			if test.valid {
				assert.NoError(t, cfg.Validate())
			} else {
				assert.Error(t, cfg.Validate())
			}
		})
	}
}
