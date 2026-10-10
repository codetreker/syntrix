package rest

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/codetreker/syntrix/internal/core/database"
	"github.com/codetreker/syntrix/internal/ctxkeys"
)

// Database API error codes
const (
	ErrCodeDatabaseNotFound    = "DATABASE_NOT_FOUND"
	ErrCodeDatabaseExists      = "DATABASE_EXISTS"
	ErrCodeDatabaseSuspended   = "DATABASE_SUSPENDED"
	ErrCodeDatabaseDeleting    = "DATABASE_DELETING"
	ErrCodeProtectedDatabase   = "PROTECTED_DATABASE"
	ErrCodeQuotaExceeded       = "QUOTA_EXCEEDED"
	ErrCodeInvalidSlug         = "INVALID_SLUG"
	ErrCodeReservedSlug        = "RESERVED_SLUG"
	ErrCodeSlugImmutable       = "SLUG_IMMUTABLE"
	ErrCodeSlugExists          = "SLUG_EXISTS"
	ErrCodeDisplayNameRequired = "DISPLAY_NAME_REQUIRED"
)

// handleCreateDatabase handles POST /api/v1/databases
func (h *Handler) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "Database service not available")
		return
	}

	var req CreateDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	userID := h.getUserID(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication required")
		return
	}

	db, err := h.database.CreateDatabase(r.Context(), userID, false, req.toCreateRequest())
	if err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toDatabaseResponse(db))
}

// handleListDatabases handles GET /api/v1/databases
func (h *Handler) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "Database service not available")
		return
	}

	userID := h.getUserID(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication required")
		return
	}

	opts := h.parseListOptions(r)

	result, err := h.database.ListDatabases(r.Context(), userID, false, opts)
	if err != nil {
		writeInternalError(w, err, "Failed to list databases")
		return
	}

	resp := ListDatabasesResponse{
		Databases: make([]*DatabaseSummaryResponse, 0, len(result.Databases)),
		Total:     result.Total,
	}

	for _, db := range result.Databases {
		resp.Databases = append(resp.Databases, toDatabaseSummary(db))
	}

	if result.Quota != nil {
		resp.Quota = &QuotaResponse{
			Used:  result.Quota.Used,
			Limit: result.Quota.Limit,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleGetDatabase handles GET /api/v1/databases/{identifier}
func (h *Handler) handleGetDatabase(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "Database service not available")
		return
	}

	identifier := r.PathValue("identifier")
	if identifier == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Database identifier is required")
		return
	}

	userID := h.getUserID(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication required")
		return
	}

	db, err := h.database.GetDatabase(r.Context(), userID, false, identifier)
	if err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toDatabaseResponse(db))
}

// handleUpdateDatabase handles PATCH /api/v1/databases/{identifier}
func (h *Handler) handleUpdateDatabase(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "Database service not available")
		return
	}

	identifier := r.PathValue("identifier")
	if identifier == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Database identifier is required")
		return
	}

	var req UpdateDatabaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	userID := h.getUserID(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication required")
		return
	}

	db, err := h.database.UpdateDatabase(r.Context(), userID, false, identifier, req.toUpdateRequest())
	if err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toDatabaseResponse(db))
}

// handleDeleteDatabase handles DELETE /api/v1/databases/{identifier}
func (h *Handler) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "Database service not available")
		return
	}

	identifier := r.PathValue("identifier")
	if identifier == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Database identifier is required")
		return
	}

	userID := h.getUserID(r)
	if userID == "" {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication required")
		return
	}

	// Get database first to return info in response
	db, err := h.database.GetDatabase(r.Context(), userID, false, identifier)
	if err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	if err := h.database.DeleteDatabase(r.Context(), userID, false, identifier); err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, DeleteDatabaseResponse{
		ID:      db.ID,
		Slug:    db.Slug,
		Status:  string(database.StatusDeleting),
		Message: "Database deletion initiated",
	})
}

// getUserID extracts the user ID from the request context
func (h *Handler) getUserID(r *http.Request) string {
	if uid, ok := r.Context().Value(ctxkeys.KeyUserID).(string); ok {
		return uid
	}
	return ""
}

// parseListOptions parses query parameters into ListOptions
func (h *Handler) parseListOptions(r *http.Request) database.ListOptions {
	opts := database.ListOptions{}

	if limit := r.URL.Query().Get("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil && l > 0 {
			opts.Limit = l
		}
	}

	if offset := r.URL.Query().Get("offset"); offset != "" {
		if o, err := strconv.Atoi(offset); err == nil && o >= 0 {
			opts.Offset = o
		}
	}

	if status := r.URL.Query().Get("status"); status != "" {
		opts.Status = database.DatabaseStatus(status)
	}

	if owner := r.URL.Query().Get("owner"); owner != "" {
		opts.OwnerID = owner
	}

	return opts
}

// writeDatabaseError writes an appropriate error response for database errors
func (h *Handler) writeDatabaseError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, database.ErrDatabaseNotFound):
		writeError(w, http.StatusNotFound, ErrCodeDatabaseNotFound, "Database not found")
	case errors.Is(err, database.ErrDatabaseExists):
		writeError(w, http.StatusConflict, ErrCodeDatabaseExists, "Database already exists")
	case errors.Is(err, database.ErrDatabaseSuspended):
		writeError(w, http.StatusForbidden, ErrCodeDatabaseSuspended, "Database is suspended")
	case errors.Is(err, database.ErrDatabaseDeleting):
		writeError(w, http.StatusGone, ErrCodeDatabaseDeleting, "Database is being deleted")
	case errors.Is(err, database.ErrProtectedDatabase):
		writeError(w, http.StatusBadRequest, ErrCodeProtectedDatabase, "Cannot delete protected database")
	case errors.Is(err, database.ErrNotOwner):
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "You do not have permission to access this database")
	case errors.Is(err, database.ErrQuotaExceeded):
		writeError(w, http.StatusForbidden, ErrCodeQuotaExceeded, "Maximum database limit reached")
	case errors.Is(err, database.ErrInvalidSlugFormat):
		writeError(w, http.StatusBadRequest, ErrCodeInvalidSlug, "Slug must start with a letter and contain only lowercase letters, numbers, and hyphens (3-63 characters)")
	case errors.Is(err, database.ErrInvalidSlugLength):
		writeError(w, http.StatusBadRequest, ErrCodeInvalidSlug, "Slug must be 3-63 characters")
	case errors.Is(err, database.ErrReservedSlug):
		writeError(w, http.StatusBadRequest, ErrCodeReservedSlug, "This slug is reserved and cannot be used")
	case errors.Is(err, database.ErrSlugImmutable):
		writeError(w, http.StatusBadRequest, ErrCodeSlugImmutable, "Slug cannot be changed once set")
	case errors.Is(err, database.ErrSlugExists):
		writeError(w, http.StatusConflict, ErrCodeSlugExists, "A database with this slug already exists")
	case errors.Is(err, database.ErrDisplayNameRequired):
		writeError(w, http.StatusBadRequest, ErrCodeDisplayNameRequired, "Display name is required")
	default:
		writeInternalError(w, err, "Database operation failed")
	}
}
