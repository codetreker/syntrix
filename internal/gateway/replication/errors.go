package replication

import (
	"context"
	"errors"
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	storagetypes "github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// Failure keeps transport-safe classification separate from its original cause.
type Failure struct {
	Status  int
	Code    string
	Message string
	Cause   error
}

func (f Failure) Error() string { return f.Code + ": " + f.Message }
func (f Failure) Unwrap() error { return f.Cause }

// ClassifyPullError preserves the source-mode distinctions of HTTP Pull.
// A canceled request has status 499 and no wire error body.
func ClassifyPullError(req storage.ReplicationPullRequest, err error) Failure {
	var classified *Failure
	if errors.As(err, &classified) {
		return *classified
	}
	var value Failure
	if errors.As(err, &value) {
		return value
	}
	failure := Failure{Cause: err}
	set := func(status int, code, message string) Failure {
		failure.Status, failure.Code, failure.Message = status, code, message
		return failure
	}
	if req.Source != nil && req.Source.Limit != nil && errors.Is(err, model.ErrQueryWorkLimit) {
		return set(http.StatusUnprocessableEntity, "QUERY_WORK_LIMIT", "Query exceeds work or size limits")
	}
	switch {
	case errors.Is(err, storagetypes.ErrReplicationWindowIncomplete):
		return set(http.StatusServiceUnavailable, "REPLICATION_WINDOW_INCOMPLETE", "Query did not produce a complete replication window")
	case errors.Is(err, indexer.ErrNoMatchingIndex):
		return set(http.StatusBadRequest, "NO_MATCHING_INDEX", "No matching index")
	case errors.Is(err, indexer.ErrIndexNotReady), errors.Is(err, indexer.ErrIndexRebuilding):
		return set(http.StatusServiceUnavailable, "INDEX_UNAVAILABLE", "Query index is unavailable")
	case errors.Is(err, storagetypes.ErrInvalidReplicationSource):
		return set(http.StatusBadRequest, "INVALID_REPLICATION_SOURCE", "Invalid replication source")
	case errors.Is(err, context.Canceled):
		return set(499, "", "")
	case errors.Is(err, context.DeadlineExceeded):
		return set(http.StatusGatewayTimeout, "DEADLINE_EXCEEDED", "Pull deadline exceeded")
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return set(http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Pull request exceeds size limit")
	}
	if errors.Is(err, model.ErrQueryWorkLimit) {
		return set(http.StatusUnprocessableEntity, "REPLICATION_BUDGET_EXCEEDED", "Replication page exceeds work or size limits")
	}
	var watch *storagetypes.WatchError
	if errors.As(err, &watch) {
		switch watch.Code {
		case storagetypes.WatchInvalidScope, storagetypes.WatchInvalidCheckpoint, storagetypes.WatchScopeMismatch:
			return set(http.StatusBadRequest, "BAD_REQUEST", "Invalid replication parameters")
		case storagetypes.WatchSourceMismatch, storagetypes.WatchHistoryUnavailable, storagetypes.WatchPayloadUnavailable:
			return set(http.StatusConflict, "RESYNC_REQUIRED", "Restart replication with an empty checkpoint")
		case storagetypes.WatchUnsupported:
			return set(http.StatusNotImplemented, "REPLICATION_UNSUPPORTED", "Replication is unsupported by this source")
		case storagetypes.WatchSourceUnavailable:
			return set(http.StatusServiceUnavailable, "REPLICATION_UNAVAILABLE", "Replication source is unavailable")
		case storagetypes.WatchPermissionDenied:
			return set(http.StatusForbidden, "FORBIDDEN", "Replication permission denied")
		}
	}
	return set(http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to pull changes")
}
