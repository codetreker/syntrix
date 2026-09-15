package types

import (
	"context"
	"fmt"
	"time"
)

// WatchCheckpoint is a Store-issued, portable encoded continuation. Callers may
// persist and transmit it, but must not interpret it or compare positions by its
// byte order. Its source identity is independent of client and Puller instances.
type WatchCheckpoint string

type WatchStartMode int

const (
	WatchStartCurrent WatchStartMode = iota
	// WatchStartForScan establishes an overlapping replay boundary that can also
	// fence committed scans through SourceScanRequest.AtLeast.
	WatchStartForScan
)

type WatchOptions struct {
	// IncludeBefore requests available before-state. It does not require retained
	// historical images or change the meaning of the current Document enrichment.
	IncludeBefore bool
	// StartMode applies only to a new stream. Resumption follows the checkpoint's
	// stored boundary, including an inclusive scan boundary when applicable.
	StartMode WatchStartMode
	// MaxAwaitTime bounds one source poll, not the lifetime of Next. Zero uses one
	// second; positive values must be at least one millisecond.
	MaxAwaitTime time.Duration
}

// Resolve validates the start contract and supplies the source polling default.
func (opts WatchOptions) Resolve(after WatchCheckpoint) (WatchOptions, error) {
	if opts.StartMode != WatchStartCurrent && opts.StartMode != WatchStartForScan {
		return WatchOptions{}, fmt.Errorf("invalid watch start mode")
	}
	if opts.StartMode != WatchStartCurrent && after != "" {
		return WatchOptions{}, fmt.Errorf("watch start mode requires an empty checkpoint")
	}
	if opts.MaxAwaitTime == 0 {
		opts.MaxAwaitTime = time.Second
	} else if opts.MaxAwaitTime < time.Millisecond {
		return WatchOptions{}, fmt.Errorf("watch maximum await time must be at least one millisecond")
	}
	return opts, nil
}

// WatchFrame advances a completed source prefix. A nil Event is ordered progress
// without a document change. Consumers save Checkpoint only after all preceding
// required work, including Event when present, succeeds.
type WatchFrame struct {
	Event      *Event
	Checkpoint WatchCheckpoint
	// CaughtUp proves that the source reached its current watermark after all
	// preceding frames. Filtering, cancellation, and budgets do not prove it.
	CaughtUp bool
	// SourceBytes counts raw bytes returned by the source for this frame, including
	// filtered events. It does not measure all work performed by the source.
	SourceBytes int64
}

// WatchStream owns one subscription, not the shared Store connection. Returned
// frames remain valid after subsequent reads. Calls to Next must be serialized;
// Close may interrupt a blocked Next. Read cancellation terminates this stream.
type WatchStream interface {
	// InitialCheckpoint is available when Watch succeeds, including on an idle
	// source. On resume it confirms the exact requested checkpoint.
	InitialCheckpoint() WatchCheckpoint
	// Next returns ordered data/progress or a terminal error. A successful frame
	// always carries a nonempty checkpoint. Errors remain visible on later reads.
	Next(context.Context) (WatchFrame, error)
	// Close cancels this subscription and reports bounded cleanup failures. It is
	// idempotent and leaves other watches and Store operations available.
	Close() error
}

type WatchErrorCode string

const (
	WatchInvalidScope       WatchErrorCode = "INVALID_SCOPE"
	WatchInvalidCheckpoint  WatchErrorCode = "INVALID_CHECKPOINT"
	WatchSourceMismatch     WatchErrorCode = "SOURCE_MISMATCH"
	WatchScopeMismatch      WatchErrorCode = "SCOPE_MISMATCH"
	WatchHistoryUnavailable WatchErrorCode = "HISTORY_UNAVAILABLE"
	WatchPayloadUnavailable WatchErrorCode = "PAYLOAD_UNAVAILABLE"
	WatchInvalidEvent       WatchErrorCode = "INVALID_EVENT"
	WatchUnsupported        WatchErrorCode = "UNSUPPORTED"
	WatchPermissionDenied   WatchErrorCode = "PERMISSION_DENIED"
	WatchSourceUnavailable  WatchErrorCode = "SOURCE_UNAVAILABLE"
)

// WatchError preserves a backend-independent reason and its original cause.
// Cancellation and deadlines remain discoverable through errors.Is.
type WatchError struct {
	Code       WatchErrorCode
	Database   string
	Collection string
	Cause      error
}

func (e *WatchError) Error() string {
	return fmt.Sprintf("watch %s for database %q collection %q", e.Code, e.Database, e.Collection)
}

func (e *WatchError) Unwrap() error { return e.Cause }
