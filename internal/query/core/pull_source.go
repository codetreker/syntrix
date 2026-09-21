package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type pullSource struct {
	filters model.Filters
	hash    string
	query   model.Query
}

// ReplicationSourceHash binds a validated query definition to its resolved
// database identity without reading documents or establishing source progress.
func ReplicationSourceHash(req types.ReplicationPullRequest) (string, error) {
	source, err := normalizePullSource(req)
	if err != nil {
		return "", err
	}
	if source == nil {
		return "", types.ErrInvalidReplicationSource
	}
	return source.hash, nil
}

func normalizePullSource(req types.ReplicationPullRequest) (*pullSource, error) {
	invalid := func(message string) (*pullSource, error) {
		return nil, fmt.Errorf("%w: %s", types.ErrInvalidReplicationSource, message)
	}
	if req.Source == nil {
		if req.RequestID != nil {
			return invalid("requestId requires a window source")
		}
		return nil, nil
	}
	source := req.Source
	if source.Version != 1 || source.Filters == nil {
		return invalid("source version 1 and filters are required")
	}
	if req.DatabaseIdentity == "" {
		return invalid("query replication requires a resolved database identity")
	}
	if source.Limit != nil {
		if *source.Limit < 1 || *source.Limit > 1000 || req.RequestID == nil || *req.RequestID == "" || !utf8.ValidString(*req.RequestID) || strings.ContainsRune(*req.RequestID, '\x00') || len(*req.RequestID) > MaxPullRequestBytes || req.Limit != 0 || req.Checkpoint != "" || req.LimitPresent || req.CheckpointPresent {
			return invalid("invalid window request fields")
		}
	} else if req.RequestID != nil {
		return invalid("requestId is only valid for window replication")
	}
	q, err := normalizeQuery(model.Query{Collection: req.Collection, Filters: source.Filters, OrderBy: source.OrderBy})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", types.ErrInvalidReplicationSource, err)
	}
	hasID := false
	for _, order := range q.OrderBy {
		hasID = hasID || order.Field == "id"
	}
	if !hasID {
		q.OrderBy = append(q.OrderBy, model.Order{Field: "id", Direction: "asc"})
	}
	queryHash, err := queryScope(req.DatabaseIdentity, q)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", types.ErrInvalidReplicationSource, err)
	}
	// Query scope normalizes predicate order and numeric equality. The outer
	// binding distinguishes source protocol and result-window semantics.
	encoded, err := json.Marshal([]any{source.Version, req.DatabaseIdentity, req.Collection, queryHash, q.OrderBy, source.Limit})
	if err != nil {
		return nil, err
	}
	filterBytes := 0
	for _, filter := range q.Filters {
		value, err := model.EncodeTypedValue(filter.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", types.ErrInvalidReplicationSource, err)
		}
		filterBytes += len(value) + len(filter.Field) + len(filter.Op)
	}
	requestIDBytes := 0
	if req.RequestID != nil {
		requestIDBytes = len(*req.RequestID)
	}
	if len(encoded)+filterBytes+len(req.Checkpoint)+len(req.Collection)+len(req.DatabaseIdentity)+requestIDBytes > MaxPullRequestBytes {
		return invalid("source exceeds request budget")
	}
	if source.Limit != nil {
		q.Limit = *source.Limit
	}
	sum := sha256.Sum256(encoded)
	return &pullSource{filters: q.Filters, hash: hex.EncodeToString(sum[:]), query: q}, nil
}

func validSourceHash(hash string) bool {
	decoded, err := hex.DecodeString(hash)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == hash
}

func validGenerationID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

func (s *pullSource) project(stored *types.StoredDoc, doc model.Document, scanning bool) (*types.ReplicationEvent, error) {
	id := doc.GetID()
	if stored == nil || stored.Deleted {
		if scanning {
			return nil, nil
		}
		return &types.ReplicationEvent{Type: types.ReplicationDelete, ID: id}, nil
	}
	matched, err := model.EvaluateFilters(s.filters, func(field string) (any, bool) {
		return indexer.DocumentField(stored, id, field)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pullError(types.WatchInvalidEvent, "source document cannot be filtered"), err)
	}
	if matched {
		return &types.ReplicationEvent{Type: types.ReplicationUpsert, Document: doc}, nil
	}
	if scanning {
		return nil, nil
	}
	return &types.ReplicationEvent{Type: types.ReplicationLeave, ID: id}, nil
}
