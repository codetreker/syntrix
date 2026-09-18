package rest

import (
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
)

const ExpectedDatabaseIdentityHeader = "X-Syntrix-Expected-Database-Identity"

const ErrCodeDatabaseIdentityMismatch = "DATABASE_IDENTITY_MISMATCH"

func expectedDatabaseIdentity(r *http.Request) (string, bool, error) {
	values, present := r.Header[http.CanonicalHeaderKey(ExpectedDatabaseIdentityHeader)]
	if !present {
		return "", false, nil
	}
	if len(values) != 1 {
		return "", true, database.ErrInvalidDatabaseID
	}
	if err := database.ValidateID(values[0]); err != nil {
		return "", true, err
	}
	return values[0], true, nil
}

// Bound reads share replication's full-database authorization before any ACL
// evaluation can prefetch document content or disclose a replacement identity.
func (h *Handler) withBoundDatabaseIdentity(next, unbound http.HandlerFunc) http.HandlerFunc {
	bound := h.protected(func(w http.ResponseWriter, r *http.Request) {
		r, ok := h.resolveReplicationDatabase(w, r, true)
		if ok {
			next(w, r)
		}
	})
	return func(w http.ResponseWriter, r *http.Request) {
		_, present, err := expectedDatabaseIdentity(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid expected database identity header")
			return
		}
		if present {
			bound(w, r)
			return
		}
		unbound(w, r)
	}
}

func (h *Handler) resolveReplicationDatabase(w http.ResponseWriter, r *http.Request, authoritative bool) (*http.Request, bool) {
	expected, bound, err := expectedDatabaseIdentity(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid expected database identity header")
		return r, false
	}
	uid, _ := r.Context().Value(identity.ContextKeyUserID).(string)
	if uid == "" {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Authentication is required")
		return r, false
	}
	if h.database == nil {
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Database validation service is unavailable")
		return r, false
	}
	identifier := r.PathValue("database")
	if identifier == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Database identifier is required in URL path")
		return r, false
	}
	var db *database.Database
	if authoritative || bound {
		db, err = h.database.ResolveDatabaseAuthoritative(r.Context(), identifier)
	} else {
		db, err = h.database.ResolveDatabase(r.Context(), identifier)
	}
	if err != nil {
		(&DatabaseValidator{}).writeDatabaseValidationError(w, identifier, err)
		return r, false
	}
	if db == nil || db.ID == "" {
		writeError(w, http.StatusInternalServerError, ErrCodeInternalError, "Database validation context is missing")
		return r, false
	}
	r = r.WithContext(database.WithDatabase(r.Context(), db))
	if !h.replicationAuthorized(w, r) {
		return r, false
	}
	if bound && db.ID != expected {
		writeError(w, http.StatusConflict, ErrCodeDatabaseIdentityMismatch, "Database identity does not match the bound database")
		return r, false
	}
	return r, true
}
