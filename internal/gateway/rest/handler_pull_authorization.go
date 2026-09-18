package rest

import (
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
)

func (h *Handler) replicationAuthorized(w http.ResponseWriter, r *http.Request) bool {
	uid, _ := r.Context().Value(identity.ContextKeyUserID).(string)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication is required")
		return false
	}
	db, ok := database.FromContext(r.Context())
	if !ok || db == nil || db.ID == "" {
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Database validation context is missing")
		return false
	}
	// Replication may expose IDs outside a query or per-document permission set.
	// Database ownership or a full-scope grant is required before data access.
	if db.OwnerID == uid {
		return true
	}
	grants, _ := r.Context().Value(identity.ContextKeyDBAdmin).([]string)
	for _, grant := range grants {
		if grant == db.ID || (db.Slug != nil && *db.Slug != "" && grant == *db.Slug) {
			return true
		}
	}
	writeError(w, http.StatusForbidden, ErrCodeForbidden, "Full database access is required for replication")
	return false
}
