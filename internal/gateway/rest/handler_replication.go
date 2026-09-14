package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	databasecore "github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	storagetypes "github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/helper"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

var validateReplicationPushFn = validateReplicationPush

func decodePullRequest(body io.Reader) (storage.ReplicationPullRequest, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return storage.ReplicationPullRequest{}, err
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
				semanticErr = &storagetypes.ReplicationError{Code: storagetypes.ReplicationHistoryUnavailable}
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
	return req, semanticErr
}

func writePullError(w http.ResponseWriter, err error) {
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
	var failure *storagetypes.ReplicationError
	if errors.As(err, &failure) {
		switch failure.Code {
		case storagetypes.ReplicationInvalidCursor, storagetypes.ReplicationScopeMismatch:
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid replication parameters")
		case storagetypes.ReplicationSourceMismatch, storagetypes.ReplicationHistoryUnavailable, storagetypes.ReplicationIdentityUnavailable:
			writeError(w, http.StatusConflict, "RESYNC_REQUIRED", "Restart replication with an empty checkpoint")
		case storagetypes.ReplicationUnsupported:
			writeError(w, http.StatusNotImplemented, "REPLICATION_UNSUPPORTED", "Replication is unsupported by this source")
		case storagetypes.ReplicationUnavailable:
			writeError(w, http.StatusServiceUnavailable, "REPLICATION_UNAVAILABLE", "Replication source is unavailable")
		case storagetypes.ReplicationPermissionDenied:
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "Replication permission denied")
		case storagetypes.ReplicationBudgetExceeded:
			writeError(w, http.StatusUnprocessableEntity, "REPLICATION_BUDGET_EXCEEDED", "Replication page exceeds work or size limits")
		default:
			writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to pull changes")
		}
		return
	}
	writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Failed to pull changes")
}

func (h *Handler) handlePull(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), storagetypes.ReplicationHardTimeout)
	defer cancel()
	req, err := decodePullRequest(http.MaxBytesReader(w, r.Body, querycore.MaxPullRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		var failure *storagetypes.ReplicationError
		if errors.As(err, &tooLarge) || errors.As(err, &failure) {
			writePullError(w, err)
		} else {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid pull request body")
		}
		return
	}
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
		writePullError(w, err)
		return
	}
	if req.Limit == 0 {
		req.Limit = storagetypes.DefaultReplicationLimit
	}
	if err := ctx.Err(); err != nil {
		writePullError(w, err)
		return
	}
	resp, err := h.engine.Pull(ctx, database, req)
	if err != nil {
		writePullError(w, err)
		return
	}
	encoded, err := wire.EncodeJSONPullPage(resp)
	if err != nil {
		writePullError(w, err)
		return
	}
	if err := ctx.Err(); err != nil {
		writePullError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request) {
	// Parse flattened push request
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

	if err := validateReplicationPushFn(pushReq); err != nil {
		slog.Warn("Push: validation error", "error", err)
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid replication parameters")
		return
	}

	slog.Info("Push: started", "collection", collection, "changes", len(changes))
	resp, err := h.engine.Push(r.Context(), database, pushReq)
	if err != nil {
		writeInternalError(w, err, "Failed to push changes")
		return
	}

	// Flatten conflicts
	flatConflicts := make([]model.Document, len(resp.Conflicts))
	for i, doc := range resp.Conflicts {
		flatConflicts[i] = flattenDocument(doc)
	}

	slog.Info("Push: completed", "collection", collection, "conflicts", len(flatConflicts))

	writeJSON(w, http.StatusOK, ReplicaPushResponse{
		Conflicts: flatConflicts,
	})
}
