package rest

import (
	"encoding/json"
	"net/http"

	"github.com/codetreker/syntrix/internal/core/database"
)

// handleAdminCreateDatabase handles POST /admin/databases
func (h *Handler) handleAdminCreateDatabase(w http.ResponseWriter, r *http.Request) {
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

	db, err := h.database.CreateDatabase(r.Context(), userID, true, req.toCreateRequest())
	if err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, toDatabaseResponse(db))
}

// handleAdminListDatabases handles GET /admin/databases
func (h *Handler) handleAdminListDatabases(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "Database service not available")
		return
	}

	opts := h.parseListOptions(r)

	result, err := h.database.ListDatabases(r.Context(), "", true, opts)
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

	writeJSON(w, http.StatusOK, resp)
}

// handleAdminGetDatabase handles GET /admin/databases/{identifier}
func (h *Handler) handleAdminGetDatabase(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "Database service not available")
		return
	}

	identifier := r.PathValue("identifier")
	if identifier == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Database identifier is required")
		return
	}

	db, err := h.database.GetDatabase(r.Context(), "", true, identifier)
	if err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toDatabaseResponse(db))
}

// handleAdminUpdateDatabase handles PATCH /admin/databases/{identifier}
func (h *Handler) handleAdminUpdateDatabase(w http.ResponseWriter, r *http.Request) {
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

	db, err := h.database.UpdateDatabase(r.Context(), "", true, identifier, req.toUpdateRequest())
	if err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, toDatabaseResponse(db))
}

// handleAdminDeleteDatabase handles DELETE /admin/databases/{identifier}
func (h *Handler) handleAdminDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	if h.database == nil {
		writeError(w, http.StatusServiceUnavailable, ErrCodeInternalError, "Database service not available")
		return
	}

	identifier := r.PathValue("identifier")
	if identifier == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Database identifier is required")
		return
	}

	// Get database first to return info in response
	db, err := h.database.GetDatabase(r.Context(), "", true, identifier)
	if err != nil {
		h.writeDatabaseError(w, err)
		return
	}

	if err := h.database.DeleteDatabase(r.Context(), "", true, identifier); err != nil {
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
