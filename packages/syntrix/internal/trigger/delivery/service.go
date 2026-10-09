package delivery

import (
	"context"
)

// Service consumes delivery tasks and executes HTTP webhooks.
type Service interface {
	// Start begins consuming and processing delivery tasks.
	// Blocks until context is cancelled.
	Start(ctx context.Context) error
}
