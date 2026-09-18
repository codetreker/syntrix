package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	databasecore "github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	storagetypes "github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/helper"
	"github.com/syntrixbase/syntrix/internal/indexer"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

var validateReplicationPushFn = validateReplicationPush

const pullResponseWriteTimeout = 10 * time.Second

func decodePullRequest(body io.Reader) (request storage.ReplicationPullRequest, failure error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return storage.ReplicationPullRequest{}, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	_, queryMode := fields["source"]
	if queryMode {
		if err := wire.ValidatePullSourceJSON(data); err != nil {
			return request, err
		}
		defer func() {
			var watchError *storagetypes.WatchError
			var tooLarge *http.MaxBytesError
			if failure != nil && !errors.As(failure, &watchError) && !errors.As(failure, &tooLarge) {
				failure = storagetypes.ErrInvalidReplicationSource
			}
		}()
	}
	if err := model.ValidateJSONUnicode(data); err != nil {
		return storage.ReplicationPullRequest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return storage.ReplicationPullRequest{}, errors.New("pull request must be an object")
	}
	var req storage.ReplicationPullRequest
	var semanticErr error
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return req, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return req, errors.New("duplicate pull request field")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return req, err
		}
		switch key {
		case "source":
			req.Source, err = wire.DecodeJSONPullSource(raw)
			if err != nil {
				return req, err
			}
		case "requestId":
			var id string
			if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &id) != nil || id == "" || len(id) > querycore.MaxPullRequestBytes {
				return req, storagetypes.ErrInvalidReplicationSource
			}
			req.RequestID = &id
		case "collection":
			if bytes.Equal(raw, []byte("null")) {
				return req, errors.New("collection must be a string")
			}
			if err := json.Unmarshal(raw, &req.Collection); err != nil {
				return req, err
			}
		case "checkpoint":
			if bytes.Equal(raw, []byte("null")) {
				continue
			}
			if len(raw) > 0 && (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')) {
				semanticErr = &storagetypes.WatchError{Code: storagetypes.WatchHistoryUnavailable}
				continue
			}
			if err := json.Unmarshal(raw, &req.Checkpoint); err != nil {
				return req, err
			}
			if len(req.Checkpoint) > querycore.MaxPullCursorBytes {
				semanticErr = &http.MaxBytesError{Limit: querycore.MaxPullCursorBytes}
			}
		case "limit":
			if bytes.Equal(raw, []byte("null")) {
				return req, errors.New("limit must be an integer")
			}
			if err := json.Unmarshal(raw, &req.Limit); err != nil {
				return req, err
			}
		default:
			return req, errors.New("unknown pull request field")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return req, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return req, errors.New("pull request must contain one object")
	}
	if req.Source != nil {
		if req.Source.Limit != nil {
			if req.RequestID == nil || seen["checkpoint"] || seen["limit"] {
				return req, storagetypes.ErrInvalidReplicationSource
			}
		} else if req.RequestID != nil {
			return req, storagetypes.ErrInvalidReplicationSource
		}
		if req.Limit < 0 || req.Limit > wire.MaxPullLimit {
			return req, storagetypes.ErrInvalidReplicationSource
		}
	} else if req.RequestID != nil {
		return req, storagetypes.ErrInvalidReplicationSource
	}
	return req, semanticErr
}

func writePullRequestError(w http.ResponseWriter, req storage.ReplicationPullRequest, err error) {
	if req.Source != nil && req.Source.Limit != nil && errors.Is(err, model.ErrQueryWorkLimit) {
		writeError(w, http.StatusUnprocessableEntity, "QUERY_WORK_LIMIT", "Query exceeds work or size limits")
		return
	}
	writePullError(w, err)
}

func writePullError(w http.ResponseWriter, err error) {
	if errors.Is(err, storagetypes.ErrReplicationWindowIncomplete) {
		writeError(w, http.StatusServiceUnavailable, "REPLICATION_WINDOW_INCOMPLETE", "Query did not produce a complete replication window")
		return
	}
	if errors.Is(err, indexer.ErrNoMatchingIndex) {
		writeError(w, http.StatusBadRequest, "NO_MATCHING_INDEX", "No matching index")
		return
	}
	if errors.Is(err, indexer.ErrIndexNotReady) || errors.Is(err, indexer.ErrIndexRebuilding) {
		writeError(w, http.StatusServiceUnavailable, "INDEX_UNAVAILABLE", "Query index is unavailable")
		return
	}

	if errors.Is(err, storagetypes.ErrInvalidReplicationSource) {
		writeError(w, http.StatusBadRequest, "INVALID_REPLICATION_SOURCE", "Invalid replication source")
		return
	}
	if errors.Is(err, context.Canceled) {
		w.WriteHeader(499)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, "DEADLINE_EXCEEDED", "Pull deadline exceeded")
		return
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, ErrCodeRequestTooLarge, "Pull request exceeds size limit")
		return
	}
	if errors.Is(err, model.ErrQueryWorkLimit) {
		writeError(w, http.StatusUnprocessableEntity, "REPLICATION_BUDGET_EXCEEDED", "Replication page exceeds work or size limits")
		return
	}
	var failure *storagetypes.WatchError
	if errors.As(err, &failure) {
		switch failure.Code {
		case storagetypes.WatchInvalidScope, storagetypes.WatchInvalidCheckpoint, storagetypes.WatchScopeMismatch:
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid replication parameters")
		case storagetypes.WatchSourceMismatch, storagetypes.WatchHistoryUnavailable, storagetypes.WatchPayloadUnavailable:
			writeError(w, http.StatusConflict, "RESYNC_REQUIRED", "Restart replication with an empty checkpoint")
		case storagetypes.WatchUnsupported:
			writeError(w, http.StatusNotImplemented, "REPLICATION_UNSUPPORTED", "Replication is unsupported by this source")
		case storagetypes.WatchSourceUnavailable:
			writeError(w, http.StatusServiceUnavailable, "REPLICATION_UNAVAILABLE", "Replication source is unavailable")
		case storagetypes.WatchPermissionDenied:
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "Replication permission denied")
		default:
			writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to pull changes")
		}
		return
	}
	writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to pull changes")
}

func (h *Handler) handlePull(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), querycore.PullHardTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	// The server's general write timeout includes handler execution. Reserve
	// time to transmit the bounded page after the processing deadline.
	if err := http.NewResponseController(w).SetWriteDeadline(deadline.Add(pullResponseWriteTimeout)); err != nil {
		slog.ErrorContext(ctx, "Failed to set replication response deadline", "error", err)
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Cannot establish replication response deadline")
		return
	}
	req, err := decodePullRequest(http.MaxBytesReader(w, r.Body, querycore.MaxPullRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		var failure *storagetypes.WatchError
		if errors.As(err, &tooLarge) || errors.As(err, &failure) || errors.Is(err, storagetypes.ErrInvalidReplicationSource) {
			writePullRequestError(w, req, err)
		} else {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid pull request body")
		}
		return
	}
	r, ok := h.resolveReplicationDatabase(w, r.WithContext(ctx), req.Source != nil)
	if !ok {
		return
	}
	ctx = r.Context()
	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}
	if current, ok := databasecore.FromContext(ctx); ok {
		req.DatabaseIdentity = current.ID
	} else if h.dbValidator == nil {
		req.DatabaseIdentity = database
	} else {
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Database validation context is missing")
		return
	}
	if err := validateReplicationPull(database, req); err != nil {
		writePullRequestError(w, req, err)
		return
	}
	if req.Limit == 0 && (req.Source == nil || req.Source.Limit == nil) {
		req.Limit = querycore.DefaultPullLimit
	}
	if err := ctx.Err(); err != nil {
		writePullRequestError(w, req, err)
		return
	}
	resp, err := h.engine.Pull(ctx, database, req)
	if err != nil {
		writePullRequestError(w, req, err)
		return
	}
	if err := querycore.ValidatePullResponseScope(req, resp); err != nil {
		writePullRequestError(w, req, err)
		return
	}
	encoded, err := wire.EncodeJSONPullPage(resp)
	if err != nil {
		writePullRequestError(w, req, err)
		return
	}
	if err := ctx.Err(); err != nil {
		writePullRequestError(w, req, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(encoded); err != nil {
		slog.WarnContext(ctx, "Failed to write replication response", "error", err)
	}
}

func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request) {
	var reqBody ReplicaPushRequest
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		slog.Warn("Push: invalid request body", "error", err)
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	collection := reqBody.Collection
	if collection == "" {
		slog.Warn("Push: change missing collection")
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Collection is required")
		return
	}

	if err := helper.CheckCollectionPath(collection); err != nil {
		slog.Warn("Push: invalid collection", "error", err)
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid collection")
		return
	}

	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}

	// Convert flattened changes to storage.ReplicationPushRequest
	var changes []storage.ReplicationPushChange
	for _, change := range reqBody.Changes {
		docData := change.Doc
		if err := docData.ValidateDocument(); err != nil {
			slog.Warn("Push: change document validation failed", "error", err)
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document in changes")
			return
		}

		docData.StripProtectedFields()

		if docData.GetID() == "" {
			slog.Warn("Push: change document missing ID, skipping")
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Document ID is required in changes")
			return
		}

		// Extract ID
		var docID = docData.GetID()
		if docID == "" {
			continue // Skip invalid
		}

		doc := storage.NewStoredDoc(database, collection, docID, docData)

		if change.Action == "delete" {
			doc.Deleted = true
		}

		changes = append(changes, storage.ReplicationPushChange{
			Action:      storage.PushAction(change.Action),
			Doc:         &doc,
			BaseVersion: change.BaseVersion,
		})
	}

	if len(changes) == 0 {
		slog.Warn("Push: no valid changes in request")
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "No valid changes to push")
		return
	}

	pushReq := storage.ReplicationPushRequest{
		Collection: collection,
		Changes:    changes,
	}

	if err := validateReplicationPushFn(database, pushReq); err != nil {
		slog.Warn("Push: validation error", "error", err)
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid replication parameters")
		return
	}

	slog.Info("Push: started", "collection", collection, "changes", len(changes))
	resp, err := h.engine.Push(r.Context(), database, pushReq)
	if err != nil {
		if errors.Is(err, model.ErrInvalidQuery) {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid replication parameters")
			return
		}
		if errors.Is(err, model.ErrQueryWorkLimit) {
			writeError(w, http.StatusUnprocessableEntity, "REPLICATION_BUDGET_EXCEEDED", "Push conflicts exceed the response size limit")
			return
		}
		writeInternalError(w, err, "Failed to push changes")
		return
	}

	if err := wire.ValidatePushResponse(database, pushReq, resp); err != nil {
		writeInternalError(w, err, "Invalid push result")
		return
	}
	conflicts := make([]ReplicaPushConflict, len(resp.Conflicts))
	for i, conflict := range resp.Conflicts {
		var encodedCurrent json.RawMessage
		if conflict.Current != nil {
			current := flattenDocument(conflict.Current)
			current["id"] = conflict.ID
			delete(current, "deleted")
			if conflict.Current.Deleted {
				current["deleted"] = true
			}
			encodedCurrent, err = model.EncodeTypedValue(current)
			if err != nil {
				writeInternalError(w, err, "Failed to encode push conflicts")
				return
			}
		}
		conflicts[i] = ReplicaPushConflict{ChangeIndex: conflict.ChangeIndex, ID: conflict.ID, Reason: string(conflict.Reason), Current: encodedCurrent}
	}
	encoded, err := json.Marshal(ReplicaPushResponse{Conflicts: conflicts})
	if err != nil {
		writeInternalError(w, err, "Failed to encode push conflicts")
		return
	}

	slog.Info("Push: completed", "collection", collection, "conflicts", len(conflicts))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(encoded); err != nil {
		slog.WarnContext(r.Context(), "Failed to write push response", "error", err)
	}
}
