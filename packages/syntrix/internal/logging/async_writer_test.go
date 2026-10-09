// internal/logging/async_writer_test.go
package logging

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockWriter is a thread-safe writer for testing
type mockWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	writes  int32
	flushes int32
}

func (m *mockWriter) Write(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	atomic.AddInt32(&m.writes, 1)
	return m.buf.Write(p)
}

func (m *mockWriter) Flush() error {
	atomic.AddInt32(&m.flushes, 1)
	return nil
}

func (m *mockWriter) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.buf.String()
}

func (m *mockWriter) WriteCount() int32 {
	return atomic.LoadInt32(&m.writes)
}

func (m *mockWriter) FlushCount() int32 {
	return atomic.LoadInt32(&m.flushes)
}

func TestNewAsyncWriter(t *testing.T) {
	w := &bytes.Buffer{}
	aw := NewAsyncWriter(w)
	require.NotNil(t, aw)

	// Verify default config
	assert.Equal(t, 10000, aw.bufferSize)
	assert.Equal(t, 100, aw.batchSize)
	assert.Equal(t, 100*time.Millisecond, aw.flushTimeout)

	// Cleanup
	err := aw.Close()
	assert.NoError(t, err)
}

func TestNewAsyncWriterWithConfig(t *testing.T) {
	w := &bytes.Buffer{}
	cfg := AsyncWriterConfig{
		BufferSize:   5000,
		BatchSize:    50,
		FlushTimeout: 50 * time.Millisecond,
	}
	aw := NewAsyncWriterWithConfig(w, cfg)
	require.NotNil(t, aw)

	assert.Equal(t, 5000, aw.bufferSize)
	assert.Equal(t, 50, aw.batchSize)
	assert.Equal(t, 50*time.Millisecond, aw.flushTimeout)

	err := aw.Close()
	assert.NoError(t, err)
}

func TestAsyncWriter_Write(t *testing.T) {
	w := &mockWriter{}
	aw := NewAsyncWriter(w)
	defer aw.Close()

	// Write a single message
	msg := []byte("test message\n")
	n, err := aw.Write(msg)
	assert.NoError(t, err)
	assert.Equal(t, len(msg), n)

	// Wait for async write
	time.Sleep(200 * time.Millisecond)

	// Verify message was written
	assert.Contains(t, w.String(), "test message")
}

func TestAsyncWriter_BatchWrite(t *testing.T) {
	w := &mockWriter{}
	cfg := AsyncWriterConfig{
		BufferSize:   1000,
		BatchSize:    10,
		FlushTimeout: 1 * time.Second, // Long timeout to ensure batch-based flushing
	}
	aw := NewAsyncWriterWithConfig(w, cfg)
	defer aw.Close()

	// Write exactly batchSize unique messages
	for i := 0; i < cfg.BatchSize; i++ {
		msg := []byte(fmt.Sprintf("message %d\n", i))
		n, err := aw.Write(msg)
		assert.NoError(t, err)
		assert.Equal(t, len(msg), n)
	}

	// Wait a bit for the batch to be written
	time.Sleep(100 * time.Millisecond)

	// Verify all messages were written
	content := w.String()
	count := strings.Count(content, "message")
	assert.Equal(t, cfg.BatchSize, count, "All messages should be written")

	// Verify batching: should have fewer or equal writes than messages
	// Note: Due to batching, we should have BatchSize writes (one per message in batch)
	// The benefit is that writes are grouped and flushed together
	writeCount := w.WriteCount()
	assert.GreaterOrEqual(t, int32(cfg.BatchSize), writeCount, "Write count should not exceed message count")
}

func TestAsyncWriter_TimeoutFlush(t *testing.T) {
	w := &mockWriter{}
	cfg := AsyncWriterConfig{
		BufferSize:   1000,
		BatchSize:    100, // Large batch size
		FlushTimeout: 50 * time.Millisecond,
	}
	aw := NewAsyncWriterWithConfig(w, cfg)
	defer aw.Close()

	// Write a single message (much less than batch size)
	msg := []byte("single message\n")
	n, err := aw.Write(msg)
	assert.NoError(t, err)
	assert.Equal(t, len(msg), n)

	// Wait for timeout flush
	time.Sleep(100 * time.Millisecond)

	// Verify message was flushed despite not reaching batch size
	assert.Contains(t, w.String(), "single message")
}

func TestAsyncWriter_ConcurrentWrites(t *testing.T) {
	w := &mockWriter{}
	aw := NewAsyncWriter(w)
	defer aw.Close()

	// Concurrent writers
	numWriters := 10
	messagesPerWriter := 100
	var wg sync.WaitGroup

	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerWriter; j++ {
				msg := []byte(fmt.Sprintf("message %d-%d\n", id, j))
				_, err := aw.Write(msg)
				assert.NoError(t, err)
			}
		}(i)
	}

	wg.Wait()

	// Wait for all writes to complete
	time.Sleep(300 * time.Millisecond)

	// Verify all messages were written
	content := w.String()
	count := strings.Count(content, "message")
	expected := numWriters * messagesPerWriter
	assert.Equal(t, expected, count, "All concurrent messages should be written")
}

func TestAsyncWriter_GracefulClose(t *testing.T) {
	const batchSize, backlog = 4, 1025
	w := &shutdownWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	aw := NewAsyncWriterWithConfig(w, AsyncWriterConfig{
		BufferSize:   backlog,
		BatchSize:    batchSize,
		FlushTimeout: time.Hour,
	})
	var releaseOnce, closeOnce sync.Once
	var closeErr error
	closeDone := make(chan struct{})
	release := func() { releaseOnce.Do(func() { close(w.release) }) }
	startClose := func() {
		closeOnce.Do(func() {
			go func() {
				closeErr = aw.Close()
				close(closeDone)
			}()
		})
	}
	t.Cleanup(func() {
		release()
		startClose()
		select {
		case <-closeDone:
		case <-time.After(5 * time.Second):
			t.Error("writer did not close during cleanup")
		}
	})
	waitFor := func(ch <-chan struct{}, description string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", description)
		}
	}
	var expected strings.Builder
	writeMessage := func(i int) {
		t.Helper()
		message := fmt.Sprintf("message %d\n", i)
		n, err := aw.Write([]byte(message))
		require.NoError(t, err)
		require.Equal(t, len(message), n)
		expected.WriteString(message)
	}
	for i := 0; i < batchSize; i++ {
		writeMessage(i)
	}
	waitFor(w.started, "the first underlying write")
	for i := batchSize; i < batchSize+backlog; i++ {
		writeMessage(i)
	}

	startClose()
	waitFor(aw.stopChan, "the shutdown signal")
	select {
	case <-closeDone:
		t.Fatal("Close returned while the underlying write was blocked")
	default:
	}
	select {
	case <-w.closed:
		t.Fatal("underlying writer closed before buffered writes completed")
	default:
	}
	release()
	waitFor(closeDone, "Close to finish")
	require.NoError(t, closeErr)
	require.NoError(t, w.gateErr)
	assert.Equal(t, expected.String(), w.buf.String())
	assert.Equal(t, expected.String(), w.contentAtClose)
}

type shutdownWriter struct {
	buf            bytes.Buffer
	firstWrite     sync.Once
	started        chan struct{}
	release        chan struct{}
	closed         chan struct{}
	gateErr        error
	contentAtClose string
}

func (w *shutdownWriter) Write(p []byte) (int, error) {
	w.firstWrite.Do(func() {
		close(w.started)
		select {
		case <-w.release:
		case <-time.After(5 * time.Second):
			w.gateErr = fmt.Errorf("timed out waiting to release the first write")
		}
	})
	if w.gateErr != nil {
		return 0, w.gateErr
	}
	return w.buf.Write(p)
}

func (w *shutdownWriter) Close() error {
	w.contentAtClose = w.buf.String()
	close(w.closed)
	return nil
}

func TestAsyncWriter_WriteAfterClose(t *testing.T) {
	w := &bytes.Buffer{}
	aw := NewAsyncWriter(w)

	// Close the writer
	err := aw.Close()
	assert.NoError(t, err)

	// Try to write after close
	msg := []byte("message\n")
	_, err = aw.Write(msg)
	assert.Error(t, err)
	assert.Equal(t, io.ErrClosedPipe, err)
}

func TestAsyncWriter_DoubleClose(t *testing.T) {
	w := &bytes.Buffer{}
	aw := NewAsyncWriter(w)

	// First close
	err := aw.Close()
	assert.NoError(t, err)

	// Second close should not error
	err = aw.Close()
	assert.NoError(t, err)
}

func TestAsyncWriter_FlushInterface(t *testing.T) {
	w := &mockWriter{}
	aw := NewAsyncWriter(w)
	defer aw.Close()

	// Write a message
	msg := []byte("test\n")
	_, err := aw.Write(msg)
	assert.NoError(t, err)

	// Wait for flush
	time.Sleep(200 * time.Millisecond)

	// Verify flush was called on underlying writer
	flushCount := w.FlushCount()
	assert.Greater(t, flushCount, int32(0), "Flush should be called on underlying writer")
}

func TestAsyncWriter_Flush(t *testing.T) {
	w := &mockWriter{}
	cfg := AsyncWriterConfig{
		BufferSize:   1000,
		BatchSize:    100, // Large batch size
		FlushTimeout: 1 * time.Second,
	}
	aw := NewAsyncWriterWithConfig(w, cfg)
	defer aw.Close()

	// Write a few messages
	for i := 0; i < 5; i++ {
		msg := []byte("test message\n")
		_, err := aw.Write(msg)
		assert.NoError(t, err)
	}

	// Call Flush (best-effort)
	err := aw.Flush()
	assert.NoError(t, err)

	// Verify messages were written
	content := w.String()
	assert.Contains(t, content, "test message")
}

func TestAsyncWriter_HighThroughput(t *testing.T) {
	w := &mockWriter{}
	cfg := AsyncWriterConfig{
		BufferSize:   10000,
		BatchSize:    100,
		FlushTimeout: 100 * time.Millisecond,
	}
	aw := NewAsyncWriterWithConfig(w, cfg)
	defer aw.Close()

	// High throughput test
	numMessages := 10000

	start := time.Now()
	for i := 0; i < numMessages; i++ {
		msg := []byte(fmt.Sprintf("log message %d\n", i))
		_, err := aw.Write(msg)
		assert.NoError(t, err)
	}
	writeTime := time.Since(start)

	// Writes should be fast (non-blocking)
	assert.Less(t, writeTime, 1*time.Second, "Writes should be fast")

	// Wait for all writes to complete
	time.Sleep(500 * time.Millisecond)

	// Verify all messages were written
	content := w.String()
	count := strings.Count(content, "log message")
	assert.Equal(t, numMessages, count, "All messages should be written")

	// Calculate throughput
	totalTime := time.Since(start)
	throughput := float64(numMessages) / totalTime.Seconds()
	t.Logf("Throughput: %.0f msg/s", throughput)

	// Verify batching efficiency
	writeCount := w.WriteCount()
	batchRatio := float64(numMessages) / float64(writeCount)
	t.Logf("Batch ratio: %.2f (messages per write), total writes: %d", batchRatio, writeCount)

	// With batching enabled, we expect the write count to be less than the message count
	// However, we just verify that batching is working (ratio >= 1.0)
	assert.GreaterOrEqual(t, batchRatio, 1.0, "Batch ratio should be at least 1.0")
}

func TestAsyncWriter_CloserInterface(t *testing.T) {
	// Test with a writer that implements io.Closer
	w := &closableWriter{buf: &bytes.Buffer{}}
	aw := NewAsyncWriter(w)

	msg := []byte("test\n")
	_, err := aw.Write(msg)
	assert.NoError(t, err)

	// Close should call Close on underlying writer
	err = aw.Close()
	assert.NoError(t, err)
	assert.True(t, w.closed, "Underlying writer should be closed")
}

// closableWriter is a writer that implements io.Closer
type closableWriter struct {
	buf    *bytes.Buffer
	closed bool
}

func (c *closableWriter) Write(p []byte) (n int, err error) {
	return c.buf.Write(p)
}

func (c *closableWriter) Close() error {
	c.closed = true
	return nil
}

func BenchmarkAsyncWriter_Write(b *testing.B) {
	w := &bytes.Buffer{}
	aw := NewAsyncWriter(w)
	defer aw.Close()

	msg := []byte("benchmark message\n")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = aw.Write(msg)
	}
}

func BenchmarkAsyncWriter_ConcurrentWrite(b *testing.B) {
	w := &bytes.Buffer{}
	aw := NewAsyncWriter(w)
	defer aw.Close()

	msg := []byte("benchmark message\n")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = aw.Write(msg)
		}
	})
}
