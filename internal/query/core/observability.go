package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type queryStats struct {
	start                                                            time.Time
	route, scopeID, planID, templateFingerprint, generationID        string
	branchCount                                                      int
	candidates, examined, sourceReads, sourceDocuments               int64
	missing, scope, deleted, predicate, position, posting, duplicate int64
}

func queryIdentity(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func queryTermination(page model.QueryPage, err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, model.ErrInvalidQuery):
		return "invalid_query"
	case errors.Is(err, model.ErrStaleCursor):
		return "stale_cursor"
	case errors.Is(err, model.ErrQueryWorkLimit):
		return "work_limit"
	case errors.Is(err, indexer.ErrNoMatchingIndex):
		return "no_complete_index_plan"
	case errors.Is(err, indexer.ErrIndexNotReady) || errors.Is(err, indexer.ErrIndexRebuilding):
		return "index_unavailable"
	case err != nil:
		return "internal_error"
	case page.NextCursor != nil:
		return "page_limit"
	default:
		return "exhausted"
	}
}

// Completion records contain counts and opaque identities only. Error messages,
// predicate values, document data and continuation positions stay out of logs.
func (s *queryStats) record(ctx context.Context, page model.QueryPage, err error) {
	logger := slog.Default()
	if !logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	logger.DebugContext(ctx, "Query page completed",
		"request_id", ctxkeys.RequestID(ctx), "route", s.route, "scope_id", s.scopeID,
		"plan_id", s.planID, "template_fingerprint", s.templateFingerprint, "generation_id", s.generationID, "branch_count", s.branchCount,
		"candidates", s.candidates, "examined", s.examined, "source_reads", s.sourceReads, "source_documents", s.sourceDocuments,
		"rejected_missing", s.missing, "rejected_scope", s.scope, "rejected_deleted", s.deleted, "rejected_predicate", s.predicate,
		"rejected_position", s.position, "rejected_posting", s.posting, "rejected_duplicate", s.duplicate,
		"returned", len(page.Documents), "duration_ms", float64(time.Since(s.start).Microseconds())/1000, "reason", queryTermination(page, err))
}
