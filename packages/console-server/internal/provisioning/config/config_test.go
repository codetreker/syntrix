package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func validConfig() Config {
	c := DefaultConfig()
	c.Mode = "kubeconfig"
	c.Kubeconfig = "cluster.yaml"
	c.Context = "development"
	c.Namespace = "test-services"
	c.InstallationID = "console-dev"
	return c
}

func TestConfigValidation(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Config)
	}{
		{"mode", func(c *Config) { c.Mode = "" }},
		{"file", func(c *Config) { c.Kubeconfig = "" }},
		{"context", func(c *Config) { c.Context = "" }},
		{"namespace", func(c *Config) { c.Namespace = "UPPER" }},
		{"installation", func(c *Config) { c.InstallationID = "" }},
		{"timeout", func(c *Config) { c.RequestTimeout = 0 }},
		{"long_timeout", func(c *Config) { c.RequestTimeout = 2 * time.Minute }},
		{"in_cluster_file", func(c *Config) { c.Mode = "in_cluster" }},
	} {
		t.Run(tt.name, func(t *testing.T) { c := validConfig(); tt.change(&c); require.Error(t, c.Validate()) })
	}
	c := validConfig()
	require.NoError(t, c.Validate())
	require.Equal(t, 10*time.Second, c.RequestTimeout)
	c.Mode = "in_cluster"
	c.Kubeconfig, c.Context = "", ""
	require.NoError(t, c.Validate())
}

func TestResolvePaths(t *testing.T) {
	c := validConfig()
	base := t.TempDir()
	require.NoError(t, c.ResolvePaths(base))
	require.Equal(t, filepath.Join(base, "cluster.yaml"), c.Kubeconfig)
	require.NoError(t, c.ResolvePaths("another"))
	require.Equal(t, filepath.Join(base, "cluster.yaml"), c.Kubeconfig)
	c.Kubeconfig = ""
	require.NoError(t, c.ResolvePaths(base))
	require.Empty(t, c.Kubeconfig)
}
