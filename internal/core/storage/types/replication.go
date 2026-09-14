package types

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReplicationSource connects committed bootstrap reads to replayable source
// progress. It is an optional capability; DocumentStore alone does not promise
// replication. Positions belong to the source and scope, never a client instance.
type ReplicationSource interface {
	// BeginBootstrap binds a committed read lower bound to an overlapping replay
	// start. Every later scan page must cover that bound on the same source.
	BeginBootstrap(ctx context.Context, database, collection string, budget ReplicationBudget) (ReplicationPosition, error)
	// ReadBootstrapPage scans committed current states in logical ID order,
	// including tombstones. Exhaustion transitions to changes at the original
	// replay start, not at scan completion. Retained history must be verified.
	ReadBootstrapPage(ctx context.Context, database, collection string, after ReplicationPosition, budget ReplicationBudget) (ReplicationPage, error)
	// ReadChangesPage materializes committed states covering each consumed source
	// position. Physical cleanup is progress only. Missing logical identity fails
	// explicitly; a filtered event never proves that the source is caught up.
	ReadChangesPage(ctx context.Context, database, collection string, after ReplicationPosition, budget ReplicationBudget) (ReplicationPage, error)
}

type ReplicationPhase string

const (
	ReplicationScan           ReplicationPhase = "scan"
	ReplicationChanges        ReplicationPhase = "changes"
	MaxReplicationCursorBytes                  = 128 << 10
	DefaultReplicationLimit                    = 100
	MaxReplicationLimit                        = 1000
	MaxReplicationSourceBytes int64            = 16 << 20
	MaxReplicationPageBytes   int64            = 20 << 20
	MaxReplicationFrames                       = 10000
	ReplicationSoftTimeout                     = 5 * time.Second
	ReplicationHardTimeout                     = 30 * time.Second
)

type ReplicationPosition struct {
	Phase  ReplicationPhase
	Opaque string
}

// ReplicationState carries logical identity separately from storage identity.
// A nil Document is valid only for a deletion whose historical metadata is no
// longer available; consumers must not invent a version or timestamps for it.
type ReplicationState struct {
	ID         string
	Collection string
	Deleted    bool
	Document   *StoredDoc
}

// ReplicationFrame.After is safe only after this state and every preceding frame
// have been handled. A nil State carries ordered source progress.
type ReplicationFrame struct {
	State *ReplicationState
	After ReplicationPosition
}

// ReplicationPage.End and CaughtUp apply only to acceptance of the entire page.
// A consumer admitting a prefix uses the last accepted frame's After and clears
// CaughtUp. Any source error discards the tentative page.
// CaughtUp means an observed source watermark, not the absence of concurrent
// writes at response time. Source positions are ordered; enriched document
// versions need not be monotonic. Repeated application must converge.
type ReplicationPage struct {
	Frames    []ReplicationFrame
	End       ReplicationPosition
	CaughtUp  bool
	EndReason ReplicationEndReason
	Usage     ReplicationUsage
}

type ReplicationEndReason string

const (
	ReplicationEndCount        ReplicationEndReason = "count"
	ReplicationEndBytes        ReplicationEndReason = "bytes"
	ReplicationEndFrames       ReplicationEndReason = "frames"
	ReplicationEndSoftDeadline ReplicationEndReason = "soft_deadline"
	ReplicationEndScan         ReplicationEndReason = "scan_end"
	ReplicationEndWatermark    ReplicationEndReason = "watermark"
)

// ReplicationBudget deadlines are absolute so boundary acquisition and subsequent
// reads share one request allowance. Zero fields select bounded defaults. Callers
// may lower caps; adapters must enforce them before accumulating native results.
type ReplicationBudget struct {
	Limit          int
	MaxSourceBytes int64
	MaxPageBytes   int64
	MaxProbeBytes  int64
	MaxFrames      int
	SoftDeadline   time.Time
	HardDeadline   time.Time
}

type ReplicationUsage struct {
	SourceBytes    int64
	PageBytes      int64
	ProbeBytes     int64
	FramesExamined int
}

type ReplicationErrorCode string

const (
	ReplicationInvalidCursor       ReplicationErrorCode = "INVALID_CURSOR"
	ReplicationScopeMismatch       ReplicationErrorCode = "SCOPE_MISMATCH"
	ReplicationSourceMismatch      ReplicationErrorCode = "SOURCE_MISMATCH"
	ReplicationHistoryUnavailable  ReplicationErrorCode = "HISTORY_UNAVAILABLE"
	ReplicationIdentityUnavailable ReplicationErrorCode = "IDENTITY_UNAVAILABLE"
	ReplicationUnsupported         ReplicationErrorCode = "UNSUPPORTED"
	ReplicationUnavailable         ReplicationErrorCode = "UNAVAILABLE"
	ReplicationPermissionDenied    ReplicationErrorCode = "PERMISSION_DENIED"
	ReplicationBudgetExceeded      ReplicationErrorCode = "BUDGET_EXCEEDED"
	ReplicationInvalidState        ReplicationErrorCode = "INVALID_STATE"
)

// ReplicationError preserves the adapter failure, including cancellation, while
// exposing a storage-independent recovery category. Opaque positions must never
// be included in error messages.
type ReplicationError struct {
	Code       ReplicationErrorCode
	Database   string
	Collection string
	Cause      error
}

func (e *ReplicationError) Error() string {
	return fmt.Sprintf("replication %s for database %q collection %q", e.Code, e.Database, e.Collection)
}

func (e *ReplicationError) Unwrap() error { return e.Cause }

func ValidateReplicationScope(database, collection string) error {
	if database == "" || strings.ContainsRune(database, '\x00') {
		return &ReplicationError{Code: ReplicationScopeMismatch, Database: database, Collection: collection, Cause: fmt.Errorf("replication requires a database")}
	}
	if err := ValidateConcreteCollection(collection); err != nil {
		return &ReplicationError{Code: ReplicationScopeMismatch, Database: database, Collection: collection, Cause: err}
	}
	return nil
}

// Validate checks the public shape; adapters must additionally verify opaque
// source incarnation, scope, causal context, and retained history.
func (p ReplicationPosition) Validate(expected ReplicationPhase) error {
	if (p.Phase != ReplicationScan && p.Phase != ReplicationChanges) || (expected != "" && p.Phase != expected) || p.Opaque == "" || len(p.Opaque) > MaxReplicationCursorBytes {
		return &ReplicationError{Code: ReplicationInvalidCursor, Cause: fmt.Errorf("invalid replication phase or cursor size")}
	}
	return nil
}

func ResolveReplicationBudget(b ReplicationBudget) (ReplicationBudget, error) {
	if b.Limit < 0 || b.Limit > MaxReplicationLimit || b.MaxSourceBytes < 0 || b.MaxSourceBytes > MaxReplicationSourceBytes || b.MaxPageBytes < 0 || b.MaxPageBytes > MaxReplicationPageBytes || b.MaxProbeBytes < 0 || b.MaxProbeBytes > MaxReplicationSourceBytes || b.MaxFrames < 0 || b.MaxFrames > MaxReplicationFrames {
		return ReplicationBudget{}, &ReplicationError{Code: ReplicationBudgetExceeded, Cause: fmt.Errorf("replication budget is outside supported bounds")}
	}
	if b.Limit == 0 {
		b.Limit = DefaultReplicationLimit
	}
	if b.MaxSourceBytes == 0 {
		b.MaxSourceBytes = MaxReplicationSourceBytes
	}
	if b.MaxPageBytes == 0 {
		b.MaxPageBytes = MaxReplicationPageBytes
	}
	if b.MaxProbeBytes == 0 {
		b.MaxProbeBytes = MaxReplicationSourceBytes
	}
	if b.MaxFrames == 0 {
		b.MaxFrames = MaxReplicationFrames
	}
	now := time.Now()
	if b.HardDeadline.IsZero() || b.HardDeadline.After(now.Add(ReplicationHardTimeout)) {
		b.HardDeadline = now.Add(ReplicationHardTimeout)
	}
	if b.SoftDeadline.IsZero() {
		b.SoftDeadline = now.Add(ReplicationSoftTimeout)
		if b.SoftDeadline.After(b.HardDeadline) {
			b.SoftDeadline = b.HardDeadline
		}
	}
	if b.SoftDeadline.After(b.HardDeadline) {
		return ReplicationBudget{}, &ReplicationError{Code: ReplicationBudgetExceeded, Cause: fmt.Errorf("replication soft deadline exceeds hard deadline")}
	}
	return b, nil
}

func (s ReplicationState) Validate(database, collection string) error {
	fail := func() error {
		return &ReplicationError{Code: ReplicationInvalidState, Database: database, Collection: collection, Cause: fmt.Errorf("replication state has inconsistent logical identity or deletion metadata")}
	}
	if s.ID == "" || strings.ContainsAny(s.ID, "/\x00") || s.Collection != collection {
		return fail()
	}
	if s.Document == nil {
		if !s.Deleted {
			return fail()
		}
		return nil
	}
	id, err := LogicalDocumentID(s.Document)
	if err != nil || id != s.ID || s.Document.Database != database || s.Document.Collection != collection || s.Document.Deleted != s.Deleted {
		return fail()
	}
	return nil
}
