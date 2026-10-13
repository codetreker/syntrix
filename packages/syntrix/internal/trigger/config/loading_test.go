package config

import (
	"testing"

	deploymentconfig "github.com/codetreker/syntrix/deployment/config"
	services "github.com/codetreker/syntrix/internal/services/config"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPrepareForLoadPreservesNonBindingDefaults(t *testing.T) {
	cfg := DefaultConfig()
	want := cfg
	cfg.PrepareForLoad()
	require.Nil(t, cfg.Broker)
	require.Empty(t, cfg.Evaluator.StreamName)
	require.Empty(t, cfg.Evaluator.StorageType)
	require.Empty(t, cfg.Delivery.StreamName)
	require.Empty(t, cfg.Delivery.ConsumerName)
	require.Empty(t, cfg.Delivery.StorageType)
	cfg.Broker = want.Broker
	cfg.Evaluator.StreamName = want.Evaluator.StreamName
	cfg.Evaluator.StorageType = want.Evaluator.StorageType
	cfg.Delivery.StreamName = want.Delivery.StreamName
	cfg.Delivery.ConsumerName = want.Delivery.ConsumerName
	cfg.Delivery.StorageType = want.Delivery.StorageType
	require.Equal(t, want, cfg)
}

func TestLoadExplicitBrokerDerivesOmittedRoutes(t *testing.T) {
	binding := deploymentconfig.DefaultOperatorBinding()
	binding.StreamName = "CUSTOM_STREAM"
	binding.SubjectPrefix = "custom.tasks"
	binding.ConsumerName = "custom-delivery"
	binding.Policy.Storage = "memory"
	content, err := yaml.Marshal(map[string]any{"broker": binding})
	require.NoError(t, err)
	cfg := DefaultConfig()
	cfg.PrepareForLoad()
	require.NoError(t, yaml.Unmarshal(content, &cfg))
	cfg.ApplyDefaults()
	require.NoError(t, cfg.Validate(services.ModeDistributed))
	require.Equal(t, binding, *cfg.Broker)
	require.Equal(t, binding.StreamName, cfg.Evaluator.StreamName)
	require.Equal(t, binding.StreamName, cfg.Delivery.StreamName)
	require.Equal(t, binding.ConsumerName, cfg.Delivery.ConsumerName)
	require.Equal(t, binding.Policy.Storage, cfg.Evaluator.StorageType)
	require.Equal(t, binding.Policy.Storage, cfg.Delivery.StorageType)
}

func TestLoadExplicitBrokerPreservesContradictions(t *testing.T) {
	binding := deploymentconfig.DefaultOperatorBinding()
	binding.StreamName = "CUSTOM_STREAM"
	binding.ConsumerName = "custom-delivery"
	binding.Policy.Storage = "memory"
	for _, extra := range []map[string]any{
		{"evaluator": map[string]any{"stream_name": "FOREIGN"}},
		{"evaluator": map[string]any{"storage_type": "file"}},
		{"delivery": map[string]any{"stream_name": "FOREIGN"}},
		{"delivery": map[string]any{"consumer_name": "foreign"}},
		{"delivery": map[string]any{"storage_type": "file"}},
	} {
		extra["broker"] = binding
		content, err := yaml.Marshal(extra)
		require.NoError(t, err)
		cfg := DefaultConfig()
		cfg.PrepareForLoad()
		require.NoError(t, yaml.Unmarshal(content, &cfg))
		cfg.ApplyDefaults()
		require.Error(t, cfg.Validate(services.ModeDistributed))
	}
}

func TestLoadMissingOrIncompleteBrokerHasNoInheritedAuthority(t *testing.T) {
	for _, item := range []struct{ content, want string }{
		{"{}", "broker is required"},
		{"broker:\n  resource_mode: operator\n", "spec_revision"},
	} {
		cfg := DefaultConfig()
		cfg.PrepareForLoad()
		require.NoError(t, yaml.Unmarshal([]byte(item.content), &cfg))
		cfg.ApplyDefaults()
		require.ErrorContains(t, cfg.Validate(services.ModeDistributed), item.want)
	}
}

func TestLoadStandaloneRestoresLocalRoutesWithoutBroker(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PrepareForLoad()
	require.NoError(t, yaml.Unmarshal([]byte("{}"), &cfg))
	cfg.ApplyDefaults()
	require.Nil(t, cfg.Broker)
	require.NoError(t, cfg.Validate(services.ModeStandalone))
	defaults := DefaultConfig()
	require.Equal(t, defaults.Evaluator, cfg.Evaluator)
	require.Equal(t, defaults.Delivery, cfg.Delivery)
}
