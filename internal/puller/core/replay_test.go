package core

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/buffer"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		input    string
		expected int64
		hasError bool
	}{
		{"", 0, false},
		{"100", 100, false},
		{"1KB", 1024, false},
		{"1KiB", 1024, false},
		{"1MB", 1024 * 1024, false},
		{"1GB", 1024 * 1024 * 1024, false},
		{"1TB", 1024 * 1024 * 1024 * 1024, false},
		{"invalid", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			size, err := parseSize(tc.input)
			if tc.hasError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, size)
			}
		})
	}
}

func TestPuller_Replay(t *testing.T) {
	// Setup Puller with a real buffer backend
	tmpDir := t.TempDir()
	cfg := config.Config{
		Buffer: config.BufferConfig{
			Path:          filepath.Join(tmpDir, "buffer"),
			BatchSize:     10,
			BatchInterval: 10 * time.Millisecond,
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	p := New(cfg, logger)

	// Manually inject a backend with a buffer
	backendName := "test-backend"
	bufPath := filepath.Join(tmpDir, "buffer", backendName)
	buf, err := buffer.New(buffer.Options{
		Path:          bufPath,
		BatchSize:     10,
		BatchInterval: 10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer buf.Close()

	// Write some events to buffer
	evt1 := &events.StoreChangeEvent{
		EventID:     "1-1-hash1",
		Timestamp:   1000,
		ClusterTime: events.ClusterTime{T: 1, I: 1},
		MgoColl:     "coll1",
		MgoDocID:    "doc1",
	}
	evt2 := &events.StoreChangeEvent{
		EventID:     "2-2-hash2",
		Timestamp:   2000,
		ClusterTime: events.ClusterTime{T: 2, I: 2},
		MgoColl:     "coll1",
		MgoDocID:    "doc2",
	}

	err = buf.Write(context.Background(), evt1, []byte("token1"))
	require.NoError(t, err)
	err = buf.Write(context.Background(), evt2, []byte("token2"))
	require.NoError(t, err)

	// Wait for flush
	require.Eventually(t, func() bool {
		e, err := buf.Read(evt2.BufferKey())
		return err == nil && e != nil
	}, 2*time.Second, 10*time.Millisecond, "failed to wait for event flush")

	p.backends[backendName] = &Backend{
		name:   backendName,
		buffer: buf,
	}

	t.Run("ReplayAll", func(t *testing.T) {
		iter, err := p.Replay(context.Background(), nil, false)
		require.NoError(t, err)
		defer iter.Close()

		assert.True(t, iter.Next())
		assert.Equal(t, "1-1-hash1", iter.Event().EventID)
		assert.Equal(t, backendName, iter.Event().Backend)

		assert.True(t, iter.Next())
		assert.Equal(t, "2-2-hash2", iter.Event().EventID)

		assert.False(t, iter.Next())
	})

	t.Run("ReplayFrom", func(t *testing.T) {
		// Replaying a progress marker includes its entire timestamp group.
		after := map[string]string{
			backendName: evt1.EventID,
		}
		iter, err := p.Replay(context.Background(), after, false)
		require.NoError(t, err)
		defer iter.Close()

		assert.True(t, iter.Next())
		assert.Equal(t, "1-1-hash1", iter.Event().EventID)
		assert.True(t, iter.Next())
		assert.Equal(t, "2-2-hash2", iter.Event().EventID)

		assert.False(t, iter.Next())
	})

	t.Run("ReplayWithCoalescing", func(t *testing.T) {
		iter, err := p.Replay(context.Background(), nil, true)
		require.NoError(t, err)
		defer iter.Close()

		// Just verify it returns an iterator and works
		assert.True(t, iter.Next())
		assert.Equal(t, "1-1-hash1", iter.Event().EventID)
		assert.True(t, iter.Next())
		assert.Equal(t, "2-2-hash2", iter.Event().EventID)
	})

	t.Run("InvalidEventID", func(t *testing.T) {
		after := map[string]string{
			backendName: "invalid-id",
		}
		_, err := p.Replay(context.Background(), after, false)
		assert.Error(t, err)
	})
}
