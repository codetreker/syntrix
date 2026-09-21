package rest

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	databasecore "github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/gateway/replication"
	"github.com/syntrixbase/syntrix/internal/helper"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

var validateReplicationPushFn = validateReplicationPush

const pullResponseWriteTimeout = 10 * time.Second

func writePullRequestError(w http.ResponseWriter, req storage.ReplicationPullRequest, err error) {
	writeReplicationFailure(w, replication.ClassifyPullError(req, err))
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
	req, err := replication.DecodePullRequest(http.MaxBytesReader(w, r.Body, querycore.MaxPullRequestBytes))
	if err != nil {
		writePullRequestError(w, req, err)
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
