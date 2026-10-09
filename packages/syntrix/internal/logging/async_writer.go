// internal/logging/async_writer.go
package logging

import (
	"io"
	"sync"
	"time"
)

// AsyncWriter wraps an io.Writer with asynchronous buffered writing
// It uses a buffered channel and a background goroutine to batch writes
// for improved throughput and reduced I/O blocking
type AsyncWriter struct {
	writer      io.Writer
	logChan     chan []byte
	flushTicker *time.Ticker
	stopChan    chan struct{}
	wg          sync.WaitGroup
	closed      bool
	mu          sync.Mutex

	// Config
	bufferSize   int
	batchSize    int
	flushTimeout time.Duration
}

// AsyncWriterConfig holds configuration for AsyncWriter
type AsyncWriterConfig struct {
	// BufferSize is the channel buffer capacity (default: 10000)
	BufferSize int
	// BatchSize is the number of entries to accumulate before writing (default: 100)
	BatchSize int
	// FlushTimeout is the max time to wait before flushing partial batch (default: 100ms)
	FlushTimeout time.Duration
}

// DefaultAsyncWriterConfig returns default configuration
func DefaultAsyncWriterConfig() AsyncWriterConfig {
	return AsyncWriterConfig{
		BufferSize:   10000,
		BatchSize:    100,
		FlushTimeout: 100 * time.Millisecond,
	}
}

// NewAsyncWriter creates a new AsyncWriter with default configuration
func NewAsyncWriter(w io.Writer) *AsyncWriter {
	return NewAsyncWriterWithConfig(w, DefaultAsyncWriterConfig())
}

// NewAsyncWriterWithConfig creates a new AsyncWriter with custom configuration
func NewAsyncWriterWithConfig(w io.Writer, cfg AsyncWriterConfig) *AsyncWriter {
	aw := &AsyncWriter{
		writer:       w,
		logChan:      make(chan []byte, cfg.BufferSize),
		flushTicker:  time.NewTicker(cfg.FlushTimeout),
		stopChan:     make(chan struct{}),
		bufferSize:   cfg.BufferSize,
		batchSize:    cfg.BatchSize,
		flushTimeout: cfg.FlushTimeout,
	}

	aw.wg.Add(1)
	go aw.writeLoop()

	return aw
}

// Write implements io.Writer interface
// It sends the data to the buffered channel for asynchronous writing
func (aw *AsyncWriter) Write(p []byte) (n int, err error) {
	aw.mu.Lock()
	if aw.closed {
		aw.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	aw.mu.Unlock()

	// Make a copy since the slice might be reused by the caller
	buf := make([]byte, len(p))
	copy(buf, p)

	// Non-blocking send with fallback to blocking
	select {
	case aw.logChan <- buf:
		// Successfully sent to channel
		return len(p), nil
	default:
		// Channel is full, block until we can send
		// In production, you might want to implement a timeout or drop policy here
		aw.logChan <- buf
		return len(p), nil
	}
}

// writeLoop is the background goroutine that performs batched writes
func (aw *AsyncWriter) writeLoop() {
	defer aw.wg.Done()

	batch := make([][]byte, 0, aw.batchSize)

	for {
		select {
		case data, ok := <-aw.logChan:
			if !ok {
				// Channel closed, flush remaining data and exit
				aw.flushBatch(batch)
				return
			}

			batch = append(batch, data)

			// Flush if batch is full
			if len(batch) >= aw.batchSize {
				aw.flushBatch(batch)
				batch = batch[:0] // Reset batch
			}

		case <-aw.flushTicker.C:
			// Timeout reached, flush partial batch if any
			if len(batch) > 0 {
				aw.flushBatch(batch)
				batch = batch[:0]
			}

		case <-aw.stopChan:
			// Stop signal received
			// Drain remaining entries in channel
			for len(aw.logChan) > 0 {
				data := <-aw.logChan
				batch = append(batch, data)

				if len(batch) >= aw.batchSize {
					aw.flushBatch(batch)
					batch = batch[:0]
				}
			}
			// Flush remaining
			if len(batch) > 0 {
				aw.flushBatch(batch)
			}
			return
		}
	}
}

// flushBatch writes all entries in the batch to the underlying writer
func (aw *AsyncWriter) flushBatch(batch [][]byte) {
	if len(batch) == 0 {
		return
	}

	// Write all logs in the batch
	for _, data := range batch {
		_, _ = aw.writer.Write(data)
	}

	// If the underlying writer supports flushing, flush it
	if flusher, ok := aw.writer.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
}

// Close gracefully shuts down the AsyncWriter
// It waits for all buffered logs to be written before returning
func (aw *AsyncWriter) Close() error {
	aw.mu.Lock()
	if aw.closed {
		aw.mu.Unlock()
		return nil
	}
	aw.closed = true
	aw.mu.Unlock()

	// Stop the ticker
	aw.flushTicker.Stop()

	// Signal stop and close channel
	close(aw.stopChan)

	// Wait for writeLoop to finish
	aw.wg.Wait()

	// Close the log channel
	close(aw.logChan)

	// Close the underlying writer if it implements io.Closer
	if closer, ok := aw.writer.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}

// Flush forces all buffered logs to be written immediately
// Note: This is best-effort and may not guarantee all logs are written
// if new logs are being written concurrently
func (aw *AsyncWriter) Flush() error {
	// Send a flush signal by temporarily stopping and resuming
	// In practice, this is a simple implementation
	// For production, you might want a dedicated flush mechanism

	// For now, just wait a bit for the flush ticker to kick in
	// A better implementation would use a flush channel
	time.Sleep(aw.flushTimeout + 10*time.Millisecond)

	return nil
}
