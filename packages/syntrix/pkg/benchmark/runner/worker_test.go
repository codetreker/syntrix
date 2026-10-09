package runner

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWorker(t *testing.T) {
	mockCli := &mockClient{}
	worker := NewWorker(1, mockCli)

	assert.NotNil(t, worker)
	assert.Equal(t, 1, worker.id)
	assert.Equal(t, mockCli, worker.client)
	assert.False(t, worker.running)
}

func TestWorker_Start(t *testing.T) {
	t.Run("successful start", func(t *testing.T) {
		mockCli := &mockClient{}
		worker := NewWorker(1, mockCli)

		ctx := context.Background()
		err := worker.Start(ctx)

		assert.NoError(t, err)
		assert.True(t, worker.IsRunning())
	})

	t.Run("already running", func(t *testing.T) {
		mockCli := &mockClient{}
		worker := NewWorker(1, mockCli)

		// Set worker as running
		worker.running = true

		err := worker.Start(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already running")
	})
}

func TestWorker_Execute(t *testing.T) {
	t.Run("successful execution", func(t *testing.T) {
		mockCli := &mockClient{}
		worker := NewWorker(1, mockCli)
		worker.running = true

		op := &mockOperation{}
		result, err := worker.Execute(context.Background(), op)

		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "mock", result.OperationType)
		assert.True(t, result.Success)
	})

	t.Run("worker not running", func(t *testing.T) {
		mockCli := &mockClient{}
		worker := NewWorker(1, mockCli)

		op := &mockOperation{}
		result, err := worker.Execute(context.Background(), op)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "not running")
	})

	t.Run("nil operation", func(t *testing.T) {
		mockCli := &mockClient{}
		worker := NewWorker(1, mockCli)
		worker.running = true

		result, err := worker.Execute(context.Background(), nil)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "cannot be nil")
	})
}

func TestWorker_Stop(t *testing.T) {
	t.Run("stop running worker", func(t *testing.T) {
		mockCli := &mockClient{}
		worker := NewWorker(1, mockCli)
		worker.running = true

		err := worker.Stop(context.Background())
		assert.NoError(t, err)
		assert.False(t, worker.IsRunning())
	})

	t.Run("stop non-running worker", func(t *testing.T) {
		mockCli := &mockClient{}
		worker := NewWorker(1, mockCli)

		err := worker.Stop(context.Background())
		assert.NoError(t, err)
		assert.False(t, worker.IsRunning())
	})
}

func TestWorker_ID(t *testing.T) {
	mockCli := &mockClient{}
	worker := NewWorker(42, mockCli)

	assert.Equal(t, 42, worker.ID())
}

func TestWorker_IsRunning(t *testing.T) {
	mockCli := &mockClient{}
	worker := NewWorker(1, mockCli)

	assert.False(t, worker.IsRunning())

	worker.running = true
	assert.True(t, worker.IsRunning())

	worker.running = false
	assert.False(t, worker.IsRunning())
}

func TestWorker_Concurrency(t *testing.T) {
	mockCli := &mockClient{}
	worker := NewWorker(1, mockCli)
	worker.running = true

	// Execute multiple operations concurrently
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			op := &mockOperation{}
			_, err := worker.Execute(context.Background(), op)
			assert.NoError(t, err)
			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}
