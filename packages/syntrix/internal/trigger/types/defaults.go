package types

import "time"

// Default timeout values for trigger processing.
const (
	// DefaultTaskTimeout is the default timeout for processing a single task.
	DefaultTaskTimeout = 30 * time.Second

	// DefaultDrainTimeout is the default timeout for draining in-flight messages during shutdown.
	DefaultDrainTimeout = 5 * time.Second

	// DefaultShutdownTimeout is the default timeout for waiting workers to finish during shutdown.
	DefaultShutdownTimeout = 10 * time.Second
)
