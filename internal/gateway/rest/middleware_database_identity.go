package rest

import (
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/gateway/replication"
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
	var constraint *string
	if bound {
		constraint = &expected
	}
	db, err := replication.AuthorizeDatabase(r.Context(), h.database, r.PathValue("database"), replicationPrincipal(r), constraint, authoritative)
	if err != nil {
		failure := replication.ClassifyPullError(storage.ReplicationPullRequest{}, err)
		if failure.Status == http.StatusInternalServerError && failure.Cause != nil {
			writeInternalError(w, failure.Cause, failure.Message)
		} else {
			writeReplicationFailure(w, failure)
		}
		return r, false
	}
	return r.WithContext(database.WithDatabase(r.Context(), db)), true
}
