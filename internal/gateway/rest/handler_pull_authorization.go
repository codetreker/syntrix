package rest

import (
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
)

func (h *Handler) pullAuthorized(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Context().Value(identity.ContextKeyUserID).(string)
		if uid == "" {
			writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication is required")
			return
		}
		db, ok := database.FromContext(r.Context())
		if !ok || db == nil || db.ID == "" {
			writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Database validation context is missing")
			return
		}
		// Pull replicates the complete collection and cannot represent documents
		// leaving a per-document permission set. It requires full database access.
		if db.OwnerID == uid {
			next(w, r)
			return
		}
		grants, _ := r.Context().Value(identity.ContextKeyDBAdmin).([]string)
		for _, grant := range grants {
			if grant == db.ID || (db.Slug != nil && *db.Slug != "" && grant == *db.Slug) {
				next(w, r)
				return
			}
		}
		writeError(w, http.StatusForbidden, ErrCodeForbidden, "Full database access is required for replication")
	}
}
