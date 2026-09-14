package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"time"

	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/helper"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

const MaxPullCursorBytes = wire.MaxPullCursorBytes
const MaxPullRequestBytes = 1 << 20

type pullCursor struct {
	Version          int                    `json:"version"`
	Database         string                 `json:"database"`
	DatabaseIdentity string                 `json:"databaseIdentity"`
	Collection       string                 `json:"collection"`
	Phase            types.ReplicationPhase `json:"phase"`
	Position         string                 `json:"position"`
}

var numericCheckpoint = regexp.MustCompile(`^[+-]?[0-9]+$`)

func pullError(code types.ReplicationErrorCode, message string) error {
	return &types.ReplicationError{Code: code, Cause: errors.New(message)}
}

func decodePullCursor(database, databaseIdentity, collection, encoded string) (types.ReplicationPosition, error) {
	if len(encoded) > MaxPullCursorBytes {
		return types.ReplicationPosition{}, pullError(types.ReplicationInvalidCursor, "checkpoint exceeds size limit")
	}
	if numericCheckpoint.MatchString(encoded) {
		return types.ReplicationPosition{}, pullError(types.ReplicationHistoryUnavailable, "timestamp checkpoints require bootstrap")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return types.ReplicationPosition{}, pullError(types.ReplicationInvalidCursor, "invalid checkpoint encoding")
	}
	var cursor pullCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return types.ReplicationPosition{}, pullError(types.ReplicationInvalidCursor, "invalid checkpoint envelope")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return types.ReplicationPosition{}, pullError(types.ReplicationInvalidCursor, "invalid checkpoint envelope")
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(data, canonical) || cursor.Version != 2 {
		return types.ReplicationPosition{}, pullError(types.ReplicationInvalidCursor, "unsupported or noncanonical checkpoint")
	}
	if cursor.Database != database || cursor.DatabaseIdentity != databaseIdentity || cursor.Collection != collection {
		return types.ReplicationPosition{}, pullError(types.ReplicationScopeMismatch, "checkpoint belongs to another scope")
	}
	position := types.ReplicationPosition{Phase: cursor.Phase, Opaque: cursor.Position}
	if err := position.Validate(""); err != nil {
		return types.ReplicationPosition{}, err
	}
	return position, nil
}

func encodePullCursor(database, databaseIdentity, collection string, position types.ReplicationPosition) (string, error) {
	if err := position.Validate(""); err != nil {
		return "", pullError(types.ReplicationInvalidState, "source returned an invalid position")
	}
	data, err := json.Marshal(pullCursor{Version: 2, Database: database, DatabaseIdentity: databaseIdentity, Collection: collection, Phase: position.Phase, Position: position.Opaque})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > MaxPullCursorBytes {
		return "", pullError(types.ReplicationBudgetExceeded, "checkpoint exceeds wire budget")
	}
	return encoded, nil
}

func pullDatabaseIdentity(database string, req types.ReplicationPullRequest) string {
	if req.DatabaseIdentity == "" {
		return database
	}
	return req.DatabaseIdentity
}

// ValidatePullRequest is shared by local and remote entry points before source work.
func ValidatePullRequest(database string, req types.ReplicationPullRequest) error {
	if err := types.ValidateReplicationScope(database, req.Collection); err != nil {
		return err
	}
	if err := types.ValidateReplicationScope(pullDatabaseIdentity(database, req), req.Collection); err != nil {
		return err
	}
	if err := helper.CheckCollectionPath(req.Collection); err != nil {
		return pullError(types.ReplicationScopeMismatch, "replication requires a collection path")
	}
	if req.Limit < 0 || req.Limit > types.MaxReplicationLimit {
		return pullError(types.ReplicationInvalidCursor, "limit must be between 0 and 1000")
	}
	if len(database)+len(req.DatabaseIdentity)+len(req.Collection)+len(req.Checkpoint) > MaxPullRequestBytes {
		return pullError(types.ReplicationInvalidCursor, "request exceeds size limit")
	}
	if req.Checkpoint != "" {
		_, err := decodePullCursor(database, pullDatabaseIdentity(database, req), req.Collection, req.Checkpoint)
		return err
	}
	return nil
}

func (e *Engine) Pull(ctx context.Context, database string, req types.ReplicationPullRequest) (response *types.ReplicationPullResponse, resultErr error) {
	start := time.Now()
	var phase types.ReplicationPhase
	var page types.ReplicationPage
	defer func() {
		reason := string(page.EndReason)
		if resultErr != nil {
			reason = "internal_error"
			var failure *types.ReplicationError
			if errors.As(resultErr, &failure) {
				reason = string(failure.Code)
			}
			if errors.Is(resultErr, context.Canceled) {
				reason = "canceled"
			}
			if errors.Is(resultErr, context.DeadlineExceeded) {
				reason = "deadline_exceeded"
			}
		}
		returned := 0
		checkpoint := ""
		if response != nil {
			returned = len(response.Documents)
			checkpoint = response.Checkpoint
			position, err := decodePullCursor(database, pullDatabaseIdentity(database, req), req.Collection, checkpoint)
			if err == nil && position != page.End {
				reason = "wire_budget"
			}
		}
		slog.DebugContext(ctx, "Replication pull completed", "request_id", ctxkeys.RequestID(ctx), "scope_id", queryIdentity(database+"\x00"+pullDatabaseIdentity(database, req)+"\x00"+req.Collection), "phase", phase, "input_checkpoint_id", queryIdentity(req.Checkpoint), "output_checkpoint_id", queryIdentity(checkpoint), "frames", len(page.Frames), "returned", returned, "reason", reason, "duration_ms", float64(time.Since(start).Microseconds())/1000)
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidatePullRequest(database, req); err != nil {
		return nil, err
	}
	source, ok := e.storage.(types.ReplicationSource)
	if !ok {
		return nil, pullError(types.ReplicationUnsupported, "store does not support replication")
	}
	budget, err := types.ResolveReplicationBudget(types.ReplicationBudget{Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(budget.HardDeadline) {
		budget.HardDeadline = deadline
		if budget.SoftDeadline.After(deadline) {
			budget.SoftDeadline = deadline
		}
	}
	ctx, cancel := context.WithDeadline(ctx, budget.HardDeadline)
	defer cancel()
	var position types.ReplicationPosition
	if req.Checkpoint == "" {
		position, err = source.BeginBootstrap(ctx, database, req.Collection, budget)
		if err == nil && position.Validate(types.ReplicationScan) != nil {
			err = pullError(types.ReplicationInvalidState, "bootstrap returned an invalid scan position")
		}
	} else {
		position, err = decodePullCursor(database, pullDatabaseIdentity(database, req), req.Collection, req.Checkpoint)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	phase = position.Phase
	if phase == types.ReplicationScan {
		page, err = source.ReadBootstrapPage(ctx, database, req.Collection, position, budget)
	} else {
		page, err = source.ReadChangesPage(ctx, database, req.Collection, position, budget)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validatePullSourcePage(database, req.Collection, position, page, budget); err != nil {
		return nil, err
	}
	return admitPullPage(ctx, database, pullDatabaseIdentity(database, req), req.Collection, position, page)
}

func validatePullSourcePage(database, collection string, start types.ReplicationPosition, page types.ReplicationPage, budget types.ReplicationBudget) error {
	invalid := func() error {
		return pullError(types.ReplicationInvalidState, "source returned an inconsistent replication page")
	}
	if page.End.Validate("") != nil || len(page.Frames) > budget.MaxFrames {
		return invalid()
	}
	u := page.Usage
	if u.SourceBytes < 0 || u.SourceBytes > budget.MaxSourceBytes || u.PageBytes < 0 || u.PageBytes > budget.MaxPageBytes || u.ProbeBytes < 0 || u.ProbeBytes > budget.MaxProbeBytes || u.FramesExamined < len(page.Frames) || u.FramesExamined > budget.MaxFrames {
		return invalid()
	}
	switch page.EndReason {
	case types.ReplicationEndScan:
		if start.Phase != types.ReplicationScan || page.End.Phase != types.ReplicationChanges || page.CaughtUp {
			return invalid()
		}
	case types.ReplicationEndWatermark:
		if start.Phase != types.ReplicationChanges || page.End.Phase != types.ReplicationChanges || !page.CaughtUp {
			return invalid()
		}
	case types.ReplicationEndCount, types.ReplicationEndBytes, types.ReplicationEndFrames, types.ReplicationEndSoftDeadline:
		if page.End.Phase != start.Phase || page.CaughtUp {
			return invalid()
		}
	default:
		return invalid()
	}
	states := 0
	metadata := page
	metadata.Frames = nil
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		return invalid()
	}
	retained := len(encodedMetadata)
	previous := start
	seenPositions := map[types.ReplicationPosition]struct{}{start: {}}
	previousID := ""
	for _, frame := range page.Frames {
		if _, duplicate := seenPositions[frame.After]; frame.After.Validate(start.Phase) != nil || duplicate {
			return invalid()
		}
		seenPositions[frame.After] = struct{}{}
		previous = frame.After
		encoded, err := json.Marshal(frame)
		if err != nil {
			return invalid()
		}
		retained += len(encoded)
		if int64(retained) > budget.MaxPageBytes {
			return invalid()
		}
		if frame.State != nil {
			states++
			if err := frame.State.Validate(database, collection); err != nil {
				return err
			}
			if start.Phase == types.ReplicationScan && frame.State.ID <= previousID {
				return invalid()
			}
			previousID = frame.State.ID
			if _, err := model.EncodeTypedValue(map[string]any(replicationDocument(frame.State))); err != nil {
				return invalid()
			}
		}
	}
	if states > budget.Limit {
		return invalid()
	}
	if _, seen := seenPositions[page.End]; len(page.Frames) > 0 && seen && page.End != previous {
		return invalid()
	}
	return nil
}

func replicationDocument(state *types.ReplicationState) model.Document {
	doc := model.Document{}
	if state.Document != nil {
		if !state.Deleted {
			for key, value := range state.Document.Data {
				doc[key] = value
			}
		}
		doc["version"] = state.Document.Version
		doc["createdAt"] = state.Document.CreatedAt
		doc["updatedAt"] = state.Document.UpdatedAt
	}
	doc["id"], doc["collection"] = state.ID, state.Collection
	delete(doc, "deleted")
	if state.Deleted {
		doc["deleted"] = true
	}
	return doc
}

func admitPullPage(ctx context.Context, database, databaseIdentity, collection string, start types.ReplicationPosition, page types.ReplicationPage) (*types.ReplicationPullResponse, error) {
	checkpoint, err := encodePullCursor(database, databaseIdentity, collection, start)
	if err != nil {
		return nil, err
	}
	response := &types.ReplicationPullResponse{Documents: []model.Document{}, Checkpoint: checkpoint}
	jsonBytes, protoBytes, accepted := 0, 0, 0
	for _, frame := range page.Frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		next, err := encodePullCursor(database, databaseIdentity, collection, frame.After)
		if err != nil {
			return nil, err
		}
		candidate := *response
		candidate.Checkpoint = next
		addedJSON, addedProto := 0, 0
		if frame.State != nil {
			doc := replicationDocument(frame.State)
			encoded, err := model.EncodeTypedValue(map[string]any(doc))
			if err != nil {
				return nil, pullError(types.ReplicationInvalidState, "replication state cannot be encoded")
			}
			addedJSON = len(encoded)
			documentSize := proto.Size(&pb.Document{Data: encoded})
			addedProto = protowire.SizeTag(1) + protowire.SizeBytes(documentSize)
			candidate.Documents = append(candidate.Documents, doc)
		}
		if err := wire.CheckPullPageSize(&candidate, jsonBytes+addedJSON, protoBytes+addedProto); err != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if accepted == 0 {
				return nil, err
			}
			return response, nil
		}
		*response = candidate
		jsonBytes += addedJSON
		protoBytes += addedProto
		accepted++
	}
	end, err := encodePullCursor(database, databaseIdentity, collection, page.End)
	if err != nil {
		return nil, err
	}
	candidate := *response
	candidate.Checkpoint, candidate.CaughtUp = end, page.CaughtUp
	if err := wire.CheckPullPageSize(&candidate, jsonBytes, protoBytes); err != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if accepted == 0 {
			return nil, err
		}
		return response, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &candidate, nil
}
