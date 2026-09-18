package core

import (
	"context"
	"slices"

	"github.com/google/uuid"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func queryWindowComplete(page model.QueryPage, limit int) error {
	if limit < 1 || len(page.Documents) > limit {
		return pullError(types.WatchInvalidEvent, "query returned an invalid window size")
	}
	if len(page.Documents) < limit && page.NextCursor != nil {
		return types.ErrReplicationWindowIncomplete
	}
	return nil
}

func (e *Engine) pullWindow(ctx context.Context, database string, req types.ReplicationPullRequest, source *pullSource) (*types.ReplicationPullResponse, error) {
	page, err := e.ExecuteQueryPage(ctx, database, source.query)
	if err != nil {
		return nil, err
	}
	if err := queryWindowComplete(page, source.query.Limit); err != nil {
		return nil, err
	}
	if !slices.Equal(page.EffectiveOrder, source.query.OrderBy) {
		return nil, pullError(types.WatchInvalidEvent, "query returned an unexpected window order")
	}
	complete, requestID := true, *req.RequestID
	response := &types.ReplicationPullResponse{
		ProtocolVersion: 1, Mode: "replace", DatabaseIdentity: req.DatabaseIdentity,
		SourceHash: source.hash, RequestID: &requestID, GenerationID: uuid.NewString(),
		Complete: &complete, EffectiveOrder: page.EffectiveOrder, Documents: page.Documents,
	}
	// A replacement is admitted only when the entire window and envelope fit.
	if _, err := wire.EncodePullPage(response); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return response, nil
}

// ValidatePullResponseScope binds window replacements to the normalized request.
func ValidatePullResponseScope(req types.ReplicationPullRequest, page *types.ReplicationPullResponse) error {
	if err := wire.ValidatePullResponseScope(req, page); err != nil {
		return err
	}
	if req.Source == nil || req.Source.Limit == nil {
		return nil
	}
	source, err := normalizePullSource(req)
	if err != nil || page.SourceHash != source.hash || !slices.Equal(page.EffectiveOrder, source.query.OrderBy) || len(page.Documents) > source.query.Limit || page.RequestID == nil || *page.RequestID != *req.RequestID {
		return pullError(types.WatchInvalidEvent, "window response does not match its source request")
	}
	return nil
}
