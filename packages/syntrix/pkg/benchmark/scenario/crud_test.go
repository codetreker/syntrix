package scenario

import (
	"context"
	"testing"
	"time"

	"github.com/codetreker/syntrix/pkg/benchmark/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCRUDScenario(t *testing.T) {
	t.Run("valid configuration", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  10,
				DocumentSize: "1KB",
			},
		}

		scenario, err := NewCRUDScenario(config)

		require.NoError(t, err)
		assert.NotNil(t, scenario)
		assert.NotNil(t, scenario.docGenerator)
		assert.NotNil(t, scenario.idGenerator)
		assert.NotNil(t, scenario.createdIDs)
	})

	t.Run("nil configuration", func(t *testing.T) {
		scenario, err := NewCRUDScenario(nil)

		assert.Error(t, err)
		assert.Nil(t, scenario)
		assert.Contains(t, err.Error(), "config cannot be nil")
	})

	t.Run("invalid document size", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  10,
				DocumentSize: "invalid",
			},
		}

		scenario, err := NewCRUDScenario(config)

		assert.Error(t, err)
		assert.Nil(t, scenario)
	})
}

func TestCRUDScenario_Name(t *testing.T) {
	config := &types.Config{
		Data: types.DataConfig{
			FieldsCount:  5,
			DocumentSize: "512B",
		},
	}

	scenario, err := NewCRUDScenario(config)
	require.NoError(t, err)

	assert.Equal(t, "crud", scenario.Name())
}

func TestCRUDScenario_Setup(t *testing.T) {
	t.Run("setup without seed data", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
				SeedData:     0,
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		env := &types.TestEnv{
			Client:  &mockClient{},
			BaseURL: "http://localhost:8080",
			Token:   "test-token",
			Config:  config,
		}

		err = scenario.Setup(context.Background(), env)
		assert.NoError(t, err)
		assert.Len(t, scenario.createdIDs, 0)
	})

	t.Run("setup with seed data", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
				SeedData:     10,
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		env := &types.TestEnv{
			Client:  &mockClient{},
			BaseURL: "http://localhost:8080",
			Token:   "test-token",
			Config:  config,
		}

		err = scenario.Setup(context.Background(), env)
		assert.NoError(t, err)
		assert.Len(t, scenario.createdIDs, 10)
	})

	t.Run("setup with client error", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
				SeedData:     5,
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		env := &types.TestEnv{
			Client:  &mockClient{createErr: true},
			BaseURL: "http://localhost:8080",
			Token:   "test-token",
			Config:  config,
		}

		err = scenario.Setup(context.Background(), env)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create seed document")
	})
}

func TestCRUDScenario_NextOperation(t *testing.T) {
	t.Run("create operation when no documents exist", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		op, err := scenario.NextOperation()
		require.NoError(t, err)
		assert.NotNil(t, op)
		assert.Equal(t, "create", op.Type())
	})

	t.Run("read operation with existing documents", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		// Add some IDs
		scenario.createdIDs = []string{"id-1", "id-2", "id-3"}
		scenario.opIndex = 1 // Next should be read

		op, err := scenario.NextOperation()
		require.NoError(t, err)
		assert.NotNil(t, op)
		assert.Equal(t, "read", op.Type())
	})

	t.Run("update operation with existing documents", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		// Add some IDs
		scenario.createdIDs = []string{"id-1", "id-2", "id-3"}
		scenario.opIndex = 2 // Next should be update

		op, err := scenario.NextOperation()
		require.NoError(t, err)
		assert.NotNil(t, op)
		assert.Equal(t, "update", op.Type())
	})

	t.Run("delete operation with existing documents", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		// Add some IDs
		scenario.createdIDs = []string{"id-1", "id-2", "id-3"}
		scenario.opIndex = 3 // Next should be delete

		initialCount := len(scenario.createdIDs)
		op, err := scenario.NextOperation()
		require.NoError(t, err)
		assert.NotNil(t, op)
		assert.Equal(t, "delete", op.Type())
		assert.Equal(t, initialCount-1, len(scenario.createdIDs))
	})

	t.Run("weighted operation selection", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
			},
			Scenario: types.ScenarioConfig{
				Operations: []types.OperationConfig{
					{Type: "create", Weight: 100},
					{Type: "read", Weight: 0},
				},
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		// Should always return create due to weight
		for i := 0; i < 10; i++ {
			op, err := scenario.NextOperation()
			require.NoError(t, err)
			assert.Equal(t, "create", op.Type())
		}
	})
}

func TestCRUDScenario_Teardown(t *testing.T) {
	t.Run("teardown without cleanup", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
				Cleanup:      false,
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		scenario.createdIDs = []string{"id-1", "id-2", "id-3"}

		env := &types.TestEnv{
			Client:  &mockClient{},
			BaseURL: "http://localhost:8080",
			Token:   "test-token",
			Config:  config,
		}

		err = scenario.Teardown(context.Background(), env)
		assert.NoError(t, err)
		assert.Len(t, scenario.createdIDs, 3, "IDs should not be cleaned up")
	})

	t.Run("teardown with cleanup", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
				Cleanup:      true,
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		scenario.createdIDs = []string{"id-1", "id-2", "id-3"}

		env := &types.TestEnv{
			Client:  &mockClient{},
			BaseURL: "http://localhost:8080",
			Token:   "test-token",
			Config:  config,
		}

		err = scenario.Teardown(context.Background(), env)
		assert.NoError(t, err)
		assert.Len(t, scenario.createdIDs, 0, "IDs should be cleaned up")
	})

	t.Run("teardown with client errors continues", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:  5,
				DocumentSize: "512B",
				Cleanup:      true,
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		scenario.createdIDs = []string{"id-1", "id-2", "id-3"}

		env := &types.TestEnv{
			Client:  &mockClient{deleteErr: true},
			BaseURL: "http://localhost:8080",
			Token:   "test-token",
			Config:  config,
		}

		// Should not return error even if deletes fail
		err = scenario.Teardown(context.Background(), env)
		assert.NoError(t, err)
		assert.Len(t, scenario.createdIDs, 0)
	})
}

func TestCRUDScenario_RegisterCreatedID(t *testing.T) {
	config := &types.Config{
		Data: types.DataConfig{
			FieldsCount:  5,
			DocumentSize: "512B",
		},
	}

	scenario, err := NewCRUDScenario(config)
	require.NoError(t, err)

	assert.Len(t, scenario.createdIDs, 0)

	scenario.RegisterCreatedID("id-1")
	assert.Len(t, scenario.createdIDs, 1)
	assert.Equal(t, "id-1", scenario.createdIDs[0])

	scenario.RegisterCreatedID("id-2")
	assert.Len(t, scenario.createdIDs, 2)
	assert.Equal(t, "id-2", scenario.createdIDs[1])
}

func TestCRUDScenario_GetCollectionName(t *testing.T) {
	t.Run("default prefix", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:      5,
				DocumentSize:     "512B",
				CollectionPrefix: "",
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		collection := scenario.getCollectionName()
		assert.Equal(t, "benchmarks", collection)
	})

	t.Run("custom prefix", func(t *testing.T) {
		config := &types.Config{
			Data: types.DataConfig{
				FieldsCount:      5,
				DocumentSize:     "512B",
				CollectionPrefix: "mytest",
			},
		}

		scenario, err := NewCRUDScenario(config)
		require.NoError(t, err)

		collection := scenario.getCollectionName()
		assert.Equal(t, "mytest", collection)
	})
}

func TestCRUDScenario_Concurrency(t *testing.T) {
	config := &types.Config{
		Data: types.DataConfig{
			FieldsCount:  5,
			DocumentSize: "512B",
		},
	}

	scenario, err := NewCRUDScenario(config)
	require.NoError(t, err)

	// Seed some IDs
	for i := 0; i < 100; i++ {
		scenario.RegisterCreatedID("id-" + string(rune(i)))
	}

	// Generate operations concurrently
	done := make(chan bool, 50)
	for i := 0; i < 50; i++ {
		go func() {
			for j := 0; j < 10; j++ {
				_, err := scenario.NextOperation()
				assert.NoError(t, err)
				time.Sleep(1 * time.Millisecond)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 50; i++ {
		<-done
	}
}
