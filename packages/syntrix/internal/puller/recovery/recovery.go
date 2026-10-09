// Package recovery provides error recovery and gap detection.
package recovery

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/codetreker/syntrix/internal/puller/events"
)

// GapThreshold is the default time gap that triggers a gap detection alert.
const GapThreshold = 5 * time.Minute

// GapDetector detects time gaps in the event stream.
type GapDetector struct {
	threshold time.Duration
	logger    *slog.Logger

	// lastEventTime is the timestamp of the last event
	lastEventTime time.Time

	// gapsDetected counts the number of gaps detected
	gapsDetected int
}

// GapDetectorOptions configures the gap detector.
type GapDetectorOptions struct {
	// Threshold is the minimum gap duration to consider as a gap.
	Threshold time.Duration

	// Logger for gap detection.
	Logger *slog.Logger
}

// NewGapDetector creates a new gap detector.
func NewGapDetector(opts GapDetectorOptions) *GapDetector {
	threshold := opts.Threshold
	if threshold == 0 {
		threshold = GapThreshold
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &GapDetector{
		threshold: threshold,
		logger:    logger.With("component", "gap-detector"),
	}
}

// RecordEvent records an event and checks for gaps.
// Returns true if a gap was detected.
func (g *GapDetector) RecordEvent(evt *events.StoreChangeEvent) bool {
	// Convert cluster time to actual time
	eventTime := time.Unix(int64(evt.ClusterTime.T), 0)

	// Skip if this is the first event
	if g.lastEventTime.IsZero() {
		g.lastEventTime = eventTime
		return false
	}

	// Check for gap
	gap := eventTime.Sub(g.lastEventTime)
	if gap >= g.threshold {
		g.gapsDetected++
		g.logger.Warn("gap detected in event stream",
			"gap", gap.String(),
			"threshold", g.threshold.String(),
			"lastEventTime", g.lastEventTime,
			"currentEventTime", eventTime,
			"eventId", evt.EventID,
		)
		g.lastEventTime = eventTime
		return true
	}

	g.lastEventTime = eventTime
	return false
}

// GapsDetected returns the number of gaps detected.
func (g *GapDetector) GapsDetected() int {
	return g.gapsDetected
}

// Reset resets the detector state.
func (g *GapDetector) Reset() {
	g.lastEventTime = time.Time{}
	g.gapsDetected = 0
}

// Action represents the action to take on error.
type Action int

const (
	// ActionNone means no action is needed.
	ActionNone Action = iota

	// ActionReconnect means reconnect the change stream.
	ActionReconnect

	// ActionRestart means restart from fresh (no checkpoint).
	ActionRestart

	// ActionFatal means a fatal error that cannot be recovered.
	ActionFatal
)

func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionReconnect:
		return "reconnect"
	case ActionRestart:
		return "restart"
	case ActionFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// CheckpointManager defines the interface for managing checkpoints.
type CheckpointManager interface {
	DeleteCheckpoint() error
}

// Handler handles change stream errors and determines recovery action.
type Handler struct {
	checkpoint CheckpointManager
	logger     *slog.Logger

	// consecutiveErrors counts consecutive errors
	consecutiveErrors int

	// maxConsecutiveErrors before giving up
	maxConsecutiveErrors int

	// resumeTokenErrors counts resume token related errors
	resumeTokenErrors int
}

// HandlerOptions configures the recovery handler.
type HandlerOptions struct {
	Checkpoint           CheckpointManager
	MaxConsecutiveErrors int
	Logger               *slog.Logger
}

// NewHandler creates a new recovery handler.
func NewHandler(opts HandlerOptions) *Handler {
	maxErrors := opts.MaxConsecutiveErrors
	if maxErrors == 0 {
		maxErrors = 10
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Handler{
		checkpoint:           opts.Checkpoint,
		logger:               logger.With("component", "recovery-handler"),
		maxConsecutiveErrors: maxErrors,
	}
}

// HandleError analyzes an error and determines the recovery action.
func (h *Handler) HandleError(err error) Action {
	if err == nil {
		h.consecutiveErrors = 0
		return ActionNone
	}

	h.consecutiveErrors++

	// Check for resume token related errors
	if isResumeTokenError(err) {
		h.resumeTokenErrors++
		h.logger.Error("resume token error",
			"error", err,
			"count", h.resumeTokenErrors,
		)
		return ActionFatal
	}

	// Check for transient errors that can be recovered with reconnect
	if isTransientError(err) {
		h.logger.Warn("transient error, will reconnect",
			"error", err,
			"consecutiveErrors", h.consecutiveErrors,
		)
		return ActionReconnect
	}

	// Check if we've exceeded max consecutive errors
	if h.consecutiveErrors >= h.maxConsecutiveErrors {
		h.logger.Error("max consecutive errors reached",
			"error", err,
			"count", h.consecutiveErrors,
		)
		return ActionFatal
	}

	// Default to reconnect for unknown errors
	h.logger.Warn("unknown error, will reconnect",
		"error", err,
		"consecutiveErrors", h.consecutiveErrors,
	)
	return ActionReconnect
}

// RecoverFromResumeTokenError deletes the checkpoint and prepares for fresh start.
func (h *Handler) RecoverFromResumeTokenError(ctx context.Context) error {
	if h.checkpoint == nil {
		return nil
	}

	h.logger.Warn("deleting checkpoint due to resume token error")
	if err := h.checkpoint.DeleteCheckpoint(); err != nil {
		return fmt.Errorf("failed to delete checkpoint: %w", err)
	}

	h.logger.Info("checkpoint deleted, will restart from fresh")
	return nil
}

// ResetErrorCount resets the consecutive error count.
func (h *Handler) ResetErrorCount() {
	h.consecutiveErrors = 0
}

// ResumeTokenErrors returns the number of resume token errors.
func (h *Handler) ResumeTokenErrors() int {
	return h.resumeTokenErrors
}

// isResumeTokenError checks if the error is related to an invalid resume token.
func isResumeTokenError(err error) bool {
	// MongoDB returns specific error codes for invalid resume tokens
	errStr := err.Error()

	// Common resume token error messages
	resumeTokenErrors := []string{
		"resume token was not found",
		"the resume token was not found",
		"resume point may no longer be in the oplog",
		"ChangeStreamHistoryLost",
		"ChangeStreamFatalError",
	}

	for _, msg := range resumeTokenErrors {
		if contains(errStr, msg) {
			return true
		}
	}

	return false
}

// isTransientError checks if the error is transient and can be recovered.
func isTransientError(err error) bool {
	errStr := err.Error()

	// Common transient error messages
	transientErrors := []string{
		"connection reset",
		"connection refused",
		"broken pipe",
		"EOF",
		"timeout",
		"context deadline exceeded",
		"network",
		"temporary failure",
	}

	for _, msg := range transientErrors {
		if contains(errStr, msg) {
			return true
		}
	}

	return false
}

// contains checks if s contains substr (case insensitive).
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if equalsIgnoreCase(s[i:i+len(substr)], substr) {
			return true
		}
	}
	return false
}

// equalsIgnoreCase compares two strings ignoring case.
func equalsIgnoreCase(a, b string) bool {
	return strings.EqualFold(a, b)
}
