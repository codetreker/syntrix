package trigger

import (
	"github.com/codetreker/syntrix/internal/core/identity"
	"github.com/codetreker/syntrix/internal/trigger/delivery/worker"
)

// DeliveryWorker is an alias for worker.DeliveryWorker interface
type DeliveryWorker = worker.DeliveryWorker

// NewDeliveryWorker creates a new DeliveryWorker.
func NewDeliveryWorker(auth identity.AuthN) DeliveryWorker {
	return worker.NewDeliveryWorker(auth, nil, worker.HTTPClientOptions{}, nil)
}
