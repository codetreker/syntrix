package trigger

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewDeliveryWorker(t *testing.T) {
	// We can pass nil for auth as we are just testing the factory function
	// and not invoking methods on the worker that might use auth.
	// If NewDeliveryWorker panics on nil, we'll need a mock.
	// Assuming it doesn't panic immediately.
	w := NewDeliveryWorker(nil)
	assert.NotNil(t, w)
}
