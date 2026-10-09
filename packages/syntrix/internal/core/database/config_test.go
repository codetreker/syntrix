package database

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	assert.Equal(t, 10, cfg.MaxDatabasesPerUser)

	// Cache settings
	assert.True(t, cfg.Cache.Enabled)
	assert.Equal(t, 5*time.Minute, cfg.Cache.TTL)
	assert.Equal(t, 1*time.Minute, cfg.Cache.NegativeTTL)
	assert.Equal(t, 1000, cfg.Cache.Size)

	// Deletion settings
	assert.True(t, cfg.Deletion.Enabled)
	assert.Equal(t, 5*time.Minute, cfg.Deletion.Interval)
	assert.Equal(t, 100, cfg.Deletion.BatchSize)
	assert.Equal(t, 10, cfg.Deletion.MaxBatchesPerCycle)
}

func TestCacheConfigSettings_ToCacheConfig(t *testing.T) {
	settings := CacheConfigSettings{
		Enabled:     true,
		TTL:         10 * time.Minute,
		NegativeTTL: 2 * time.Minute,
		Size:        500,
	}

	cfg := settings.ToCacheConfig()

	assert.Equal(t, 500, cfg.Size)
	assert.Equal(t, 10*time.Minute, cfg.TTL)
	assert.Equal(t, 2*time.Minute, cfg.NegativeTTL)
}

func TestConfig_ApplyEnvOverrides(t *testing.T) {
	// Save original env vars
	origMaxPerUser := os.Getenv("DATABASE_MAX_PER_USER")
	origDeletionEnabled := os.Getenv("DATABASE_DELETION_ENABLED")
	origDeletionInterval := os.Getenv("DATABASE_DELETION_INTERVAL")
	defer func() {
		os.Setenv("DATABASE_MAX_PER_USER", origMaxPerUser)
		os.Setenv("DATABASE_DELETION_ENABLED", origDeletionEnabled)
		os.Setenv("DATABASE_DELETION_INTERVAL", origDeletionInterval)
	}()

	t.Run("override max databases per user", func(t *testing.T) {
		cfg := DefaultConfig()
		os.Setenv("DATABASE_MAX_PER_USER", "25")
		os.Setenv("DATABASE_DELETION_ENABLED", "")
		os.Setenv("DATABASE_DELETION_INTERVAL", "")

		cfg.ApplyEnvOverrides()

		assert.Equal(t, 25, cfg.MaxDatabasesPerUser)
	})

	t.Run("override deletion enabled with true", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Deletion.Enabled = false
		os.Setenv("DATABASE_MAX_PER_USER", "")
		os.Setenv("DATABASE_DELETION_ENABLED", "true")
		os.Setenv("DATABASE_DELETION_INTERVAL", "")

		cfg.ApplyEnvOverrides()

		assert.True(t, cfg.Deletion.Enabled)
	})

	t.Run("override deletion enabled with 1", func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.Deletion.Enabled = false
		os.Setenv("DATABASE_MAX_PER_USER", "")
		os.Setenv("DATABASE_DELETION_ENABLED", "1")
		os.Setenv("DATABASE_DELETION_INTERVAL", "")

		cfg.ApplyEnvOverrides()

		assert.True(t, cfg.Deletion.Enabled)
	})

	t.Run("override deletion enabled with false", func(t *testing.T) {
		cfg := DefaultConfig()
		os.Setenv("DATABASE_MAX_PER_USER", "")
		os.Setenv("DATABASE_DELETION_ENABLED", "false")
		os.Setenv("DATABASE_DELETION_INTERVAL", "")

		cfg.ApplyEnvOverrides()

		assert.False(t, cfg.Deletion.Enabled)
	})

	t.Run("override deletion interval", func(t *testing.T) {
		cfg := DefaultConfig()
		os.Setenv("DATABASE_MAX_PER_USER", "")
		os.Setenv("DATABASE_DELETION_ENABLED", "")
		os.Setenv("DATABASE_DELETION_INTERVAL", "10m")

		cfg.ApplyEnvOverrides()

		assert.Equal(t, 10*time.Minute, cfg.Deletion.Interval)
	})

	t.Run("invalid max per user is ignored", func(t *testing.T) {
		cfg := DefaultConfig()
		os.Setenv("DATABASE_MAX_PER_USER", "not-a-number")
		os.Setenv("DATABASE_DELETION_ENABLED", "")
		os.Setenv("DATABASE_DELETION_INTERVAL", "")

		cfg.ApplyEnvOverrides()

		assert.Equal(t, 10, cfg.MaxDatabasesPerUser) // default value
	})

	t.Run("invalid deletion interval is ignored", func(t *testing.T) {
		cfg := DefaultConfig()
		os.Setenv("DATABASE_MAX_PER_USER", "")
		os.Setenv("DATABASE_DELETION_ENABLED", "")
		os.Setenv("DATABASE_DELETION_INTERVAL", "not-a-duration")

		cfg.ApplyEnvOverrides()

		assert.Equal(t, 5*time.Minute, cfg.Deletion.Interval) // default value
	})
}

func TestDeletionConfig_ToDeletionWorkerConfig(t *testing.T) {
	dc := DeletionConfig{
		Enabled:            true,
		Interval:           10 * time.Minute,
		BatchSize:          200,
		MaxBatchesPerCycle: 20,
	}

	cfg := dc.ToDeletionWorkerConfig()

	assert.Equal(t, 10*time.Minute, cfg.Interval)
	assert.Equal(t, 200, cfg.BatchSize)
	assert.Equal(t, 20, cfg.MaxBatchesPerCycle)
}
