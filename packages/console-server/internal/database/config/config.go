package config

import (
	"fmt"
	"time"
)

type Config struct {
	DSN                string        `yaml:"dsn"`
	MaxOpenConnections int           `yaml:"max_open_connections"`
	ConnectTimeout     time.Duration `yaml:"connect_timeout"`
}

func DefaultConfig() Config {
	return Config{MaxOpenConnections: 10, ConnectTimeout: 10 * time.Second}
}

func (c Config) Validate() error {
	if c.DSN == "" {
		return fmt.Errorf("database.dsn is required")
	}
	if c.MaxOpenConnections < 3 {
		return fmt.Errorf("database.max_open_connections must be at least 3")
	}
	if c.ConnectTimeout <= 0 {
		return fmt.Errorf("database.connect_timeout must be positive")
	}
	return nil
}
