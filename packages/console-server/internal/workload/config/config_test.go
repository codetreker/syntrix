package config

import (
	"testing"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/workload/types"
	"github.com/stretchr/testify/require"
)

func validConfig() Config {
	cfg := DefaultConfig()
	cfg.Templates = []types.Template{{ID: "http-test", Image: "nginx:1.27", Port: 80, CPURequest: "10m", CPULimit: "100m", MemoryRequest: "16Mi", MemoryLimit: "32Mi", ProbePath: "/", Env: map[string]string{"MODE": "test"}}}
	return cfg
}

func TestConfigValidate(t *testing.T) {
	require.NoError(t, validConfig().Validate())
	for _, test := range []struct {
		name   string
		change func(*Config)
	}{
		{"missing templates", func(c *Config) { c.Templates = nil }},
		{"duplicate template", func(c *Config) { c.Templates = append(c.Templates, c.Templates[0]) }},
		{"invalid identifier", func(c *Config) { c.Templates[0].ID = "test template" }},
		{"missing image", func(c *Config) { c.Templates[0].Image = " " }},
		{"unversioned image", func(c *Config) { c.Templates[0].Image = "nginx" }},
		{"latest image", func(c *Config) { c.Templates[0].Image = "nginx:latest" }},
		{"registry port is not image tag", func(c *Config) { c.Templates[0].Image = "registry:5000/nginx" }},
		{"invalid image digest", func(c *Config) { c.Templates[0].Image = "nginx@sha256:bad" }},
		{"invalid port", func(c *Config) { c.Templates[0].Port = 65536 }},
		{"non HTTP probe", func(c *Config) { c.Templates[0].ProbePath = "health" }},
		{"probe query", func(c *Config) { c.Templates[0].ProbePath = "/?token=secret" }},
		{"invalid environment name", func(c *Config) { c.Templates[0].Env = map[string]string{"INVALID-NAME": "value"} }},
		{"bad CPU quantity", func(c *Config) { c.Templates[0].CPURequest = "ten" }},
		{"CPU above limit", func(c *Config) { c.Templates[0].CPURequest = "1" }},
		{"zero memory", func(c *Config) { c.Templates[0].MemoryRequest = "0" }},
		{"memory above limit", func(c *Config) { c.Templates[0].MemoryLimit = "1Mi" }},
		{"negative interval", func(c *Config) { c.ReconcileInterval = -time.Second }},
		{"zero timeout", func(c *Config) { c.OperationTimeout = 0 }},
		{"unbounded timeout", func(c *Config) { c.OperationTimeout = 3 * time.Minute }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.change(&cfg)
			require.Error(t, cfg.Validate())
		})
	}
}

func TestVersionedImageReferences(t *testing.T) {
	for _, image := range []string{"nginx:1.27", "registry:5000/nginx:1.27", "nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"} {
		require.True(t, versionedImage(image), image)
	}
}

func TestApplyDefaultsPreservesExplicitDurations(t *testing.T) {
	cfg := Config{OperationTimeout: time.Minute}
	cfg.ApplyDefaults()
	require.Equal(t, 2*time.Second, cfg.ReconcileInterval)
	require.Equal(t, time.Minute, cfg.OperationTimeout)
}
