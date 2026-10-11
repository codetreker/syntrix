package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestConfigValidate(t *testing.T) {
	valid := func() Config {
		cfg := DefaultConfig()
		cfg.Developers = []Developer{{ID: "dev_alice", Username: "Alice", Password: "alice-password"}}
		return cfg
	}
	tests := []struct {
		name   string
		change func(*Config)
		want   string
	}{
		{name: "valid", change: func(*Config) {}},
		{name: "no accounts", change: func(c *Config) { c.Developers = nil }, want: "developers must contain"},
		{name: "empty id", change: func(c *Config) { c.Developers[0].ID = "" }, want: ".id must"},
		{name: "id whitespace", change: func(c *Config) { c.Developers[0].ID = " dev" }, want: ".id must"},
		{name: "id NUL", change: func(c *Config) { c.Developers[0].ID = "dev\x00" }, want: ".id must"},
		{name: "id invalid UTF-8", change: func(c *Config) { c.Developers[0].ID = "dev\xff" }, want: ".id must"},
		{name: "long id", change: func(c *Config) { c.Developers[0].ID = strings.Repeat("x", 129) }, want: ".id must"},
		{name: "empty username", change: func(c *Config) { c.Developers[0].Username = " " }, want: ".username must"},
		{name: "username NUL", change: func(c *Config) { c.Developers[0].Username = "alice\x00" }, want: ".username must"},
		{name: "username invalid UTF-8", change: func(c *Config) { c.Developers[0].Username = "alice\xff" }, want: ".username must"},
		{name: "long username", change: func(c *Config) { c.Developers[0].Username = strings.Repeat("x", 129) }, want: ".username must"},
		{name: "empty password", change: func(c *Config) { c.Developers[0].Password = "" }, want: ".password must"},
		{name: "long password", change: func(c *Config) { c.Developers[0].Password = strings.Repeat("x", 73) }, want: ".password must"},
		{name: "bcrypt byte limit", change: func(c *Config) { c.Developers[0].Password = strings.Repeat("é", 37) }, want: ".password must"},
		{name: "duplicate id", change: func(c *Config) {
			c.Developers = append(c.Developers, Developer{ID: "dev_alice", Username: "bob", Password: "bob-password"})
		}, want: ".id duplicates"},
		{name: "duplicate normalized username", change: func(c *Config) {
			c.Developers = append(c.Developers, Developer{ID: "dev_bob", Username: " ALICE ", Password: "bob-password"})
		}, want: ".username duplicates"},
		{name: "zero TTL", change: func(c *Config) { c.SessionTTL = 0 }, want: "session_ttl must"},
		{name: "negative TTL", change: func(c *Config) { c.SessionTTL = -time.Second }, want: "session_ttl must"},
		{name: "long TTL", change: func(c *Config) { c.SessionTTL = 31 * 24 * time.Hour }, want: "session_ttl must"},
		{name: "maximum password", change: func(c *Config) { c.Developers[0].Password = strings.Repeat("x", 72) }},
		{name: "maximum TTL", change: func(c *Config) { c.SessionTTL = 30 * 24 * time.Hour }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := valid()
			test.change(&cfg)
			err := cfg.Validate()
			if test.want == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, test.want)
			}
		})
	}
}

func TestNormalizeUsername(t *testing.T) {
	assert.Equal(t, "alice", NormalizeUsername(" ALICE\t"))
	assert.Equal(t, "álice", NormalizeUsername("Álice"))
}
