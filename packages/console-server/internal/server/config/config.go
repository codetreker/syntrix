package config

import (
	"fmt"
	"net/url"
	"time"
)

type Config struct {
	Address         string        `yaml:"address"`
	PublicOrigin    string        `yaml:"public_origin"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	RequestTimeout  time.Duration `yaml:"request_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

func DefaultConfig() Config {
	return Config{Address: "127.0.0.1:8090", PublicOrigin: "http://localhost:8090", ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, RequestTimeout: 25 * time.Second, ShutdownTimeout: 15 * time.Second}
}

func (c Config) Validate() error {
	u, err := url.Parse(c.PublicOrigin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("server.public_origin must be an HTTP(S) origin without a path")
	}
	if u.Scheme == "http" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" {
		return fmt.Errorf("server.public_origin requires HTTPS outside localhost")
	}
	if c.Address == "" || c.ReadTimeout <= 0 || c.WriteTimeout <= 0 || c.RequestTimeout <= 0 || c.RequestTimeout >= c.WriteTimeout || c.ShutdownTimeout <= 0 {
		return fmt.Errorf("server address and positive timeouts are required")
	}
	return nil
}
