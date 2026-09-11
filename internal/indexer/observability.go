package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
)

// ProjectionLimitError names the exhausted budget without retaining document data.
type ProjectionLimitError struct {
	Kind  string
	Limit int
}

func (e *ProjectionLimitError) Error() string {
	return fmt.Sprintf("projection %s limit %d: %v", e.Kind, e.Limit, store.ErrWorkLimit)
}
func (e *ProjectionLimitError) Unwrap() error { return store.ErrWorkLimit }

func indexIdentity(parts ...string) string {
	encoded, _ := json.Marshal(parts)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func indexFailureCategory(err error, stage string) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, store.ErrWorkLimit):
		return "projection_limit"
	case temporaryBoundaryError(err):
		return "capture_unavailable"
	default:
		return stage
	}
}

func (s *service) logProjectionFailure(ctx context.Context, operationID string, evt *ChangeEvent, ref store.QueryIndexRef, stage string, cause error) {
	attrs := []any{"category", indexFailureCategory(cause, stage), "template_fingerprint", ref.TemplateFingerprint, "generation", ref.Generation}
	if operationID != "" {
		attrs = append(attrs, "operation_id", operationID)
	}
	if evt != nil {
		if evt.EventID != "" {
			attrs = append(attrs, "event_id", evt.EventID)
		}
		attrs = append(attrs, "backend", evt.Backend)
		if doc := evt.FullDocument; doc != nil {
			attrs = append(attrs, "scope_id", indexIdentity(doc.Database, doc.Collection))
			if id, err := types.LogicalDocumentID(doc); err == nil {
				attrs = append(attrs, "document_id", indexIdentity(doc.Database, doc.Collection, id))
			}
		}
	}
	var limit *ProjectionLimitError
	if errors.As(cause, &limit) {
		attrs = append(attrs, "limit_name", limit.Kind, "limit", limit.Limit)
	}
	s.logger.ErrorContext(ctx, "index projection failed", attrs...)
}

type bootstrapObservation struct {
	operationID, generation, scopeID, phase string
	databases, collections                  int
	scanned, projected, postings            int64
}

func (s *service) logBootstrap(ctx context.Context, observation *bootstrapObservation, state string, cause error) {
	attrs := []any{"operation_id", observation.operationID, "generation", observation.generation, "scope_id", observation.scopeID, "state", state, "phase", observation.phase, "databases", observation.databases, "collections_scanned", observation.collections, "documents_scanned", observation.scanned, "documents_projected", observation.projected, "postings", observation.postings}
	level := slog.LevelInfo
	if cause != nil {
		level = slog.LevelError
		attrs = append(attrs, "category", indexFailureCategory(cause, observation.phase+"_failure"))
	}
	s.logger.Log(ctx, level, "index bootstrap state", attrs...)
}
