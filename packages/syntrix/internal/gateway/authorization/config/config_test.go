package config

import (
	"path/filepath"
	"testing"

	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_Lifecycle(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want string
	}{
		{name: "defaults", want: filepath.Join("configs", "security_rules")},
		{name: "relative", path: "custom_rules.yaml", want: filepath.Join("configs", "custom_rules.yaml")},
		{name: "absolute", path: filepath.Join(t.TempDir(), "rules")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{RulesPath: tc.path}
			cfg.ApplyDefaults()
			cfg.ApplyEnvOverrides()
			cfg.ResolvePaths("configs", "data")
			want := tc.want
			if tc.name == "absolute" {
				want = tc.path
			}
			assert.Equal(t, want, cfg.RulesPath)
			for _, mode := range []services.DeploymentMode{services.ModeStandalone, services.ModeDistributed} {
				require.NoError(t, cfg.Validate(mode))
			}
		})
	}
	assert.Equal(t, "security_rules", DefaultConfig().RulesPath)
}

func TestConfig_EmptyPath(t *testing.T) {
	cfg := Config{}
	cfg.ResolvePaths("configs", "data")
	assert.Empty(t, cfg.RulesPath)
	for _, mode := range []services.DeploymentMode{services.ModeStandalone, services.ModeDistributed} {
		require.EqualError(t, cfg.Validate(mode), "gateway.authz.rules_path is required")
	}
}
