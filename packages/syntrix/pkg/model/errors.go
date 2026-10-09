package model

import (
	"context"
	"errors"
	"strings"
)

var (
	// ErrNotFound is returned when a document is not found
	ErrNotFound = errors.New("document not found")
	// ErrExists is returned when trying to create a document that already exists
	ErrExists = errors.New("document already exists")
	// ErrPreconditionFailed is returned when a CAS operation fails due to unmet preconditions
	ErrPreconditionFailed = errors.New("precondition failed")
	// ErrUnauthorized is returned when authentication fails
	ErrPermissionDenied = errors.New("permission denied")
	// ErrInvalidQuery is returned when a query is malformed
	ErrInvalidQuery = errors.New("invalid query")
	// ErrStaleCursor requires restarting a query against its current index generation.
	ErrStaleCursor = errors.New("stale query cursor")
	// ErrQueryWorkLimit rejects a page that exceeds its execution or encoding budget.
	ErrQueryWorkLimit = errors.New("query work limit exceeded")
	// ErrIndexNotReady is returned when the index layer is unavailable or rebuilding.
	// This error is a placeholder for future index layer implementation (Task 015).
	ErrIndexNotReady = errors.New("index not ready")
	// ErrCanceled is returned when the operation is canceled by the client
	ErrCanceled = errors.New("operation canceled")
)

// WrapError wraps storage errors to model errors.
// It converts context.Canceled and context.DeadlineExceeded to ErrCanceled.
func WrapError(err error) error {
	if err == nil {
		return nil
	}
	if IsCanceled(err) {
		return ErrCanceled
	}
	return err
}

// IsCanceled returns true if the error is due to context cancellation or deadline exceeded.
// It checks both direct context errors and wrapped errors (e.g., from MongoDB driver).
func IsCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, ErrCanceled) {
		return true
	}
	// Check for wrapped context errors (e.g., from MongoDB driver)
	errStr := err.Error()
	return strings.Contains(errStr, "context canceled") || strings.Contains(errStr, "context deadline exceeded")
}
