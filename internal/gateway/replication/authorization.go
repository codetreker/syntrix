package replication

import (
	"context"
	"errors"
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type Principal struct {
	Subject string
	DBAdmin []string
}

// AuthorizeDatabase leaves namespace unchanged and checks grants against the
// resolved object before comparing identities, so an unauthorized caller cannot
// discover a replacement database's identity. Bound access always bypasses caches.
func AuthorizeDatabase(ctx context.Context, service database.Service, namespace string, principal Principal, expected *string, authoritative bool) (*database.Database, error) {
	fail := func(status int, code, message string, cause error) (*database.Database, error) {
		return nil, &Failure{Status: status, Code: code, Message: message, Cause: cause}
	}
	if expected != nil {
		if err := database.ValidateID(*expected); err != nil {
			return fail(http.StatusBadRequest, "BAD_REQUEST", "Invalid expected database identity", err)
		}
	}
	if principal.Subject == "" {
		return fail(http.StatusUnauthorized, "UNAUTHORIZED", "Authentication is required", nil)
	}
	if service == nil {
		return fail(http.StatusInternalServerError, "INTERNAL_ERROR", "Database validation service is unavailable", nil)
	}
	if namespace == "" {
		return fail(http.StatusBadRequest, "BAD_REQUEST", "Database identifier is required in URL path", nil)
	}
	var db *database.Database
	var err error
	if authoritative || expected != nil {
		db, err = service.ResolveDatabaseAuthoritative(ctx, namespace)
	} else {
		db, err = service.ResolveDatabase(ctx, namespace)
	}
	if err != nil {
		switch {
		case errors.Is(err, database.ErrDatabaseNotFound):
			return fail(http.StatusNotFound, "DATABASE_NOT_FOUND", "Database '"+namespace+"' does not exist", err)
		case errors.Is(err, database.ErrDatabaseSuspended):
			return fail(http.StatusForbidden, "DATABASE_SUSPENDED", "Database '"+namespace+"' is suspended", err)
		case errors.Is(err, database.ErrDatabaseDeleting):
			return fail(http.StatusGone, "DATABASE_DELETING", "Database '"+namespace+"' is being deleted", err)
		case model.IsCanceled(err):
			return fail(499, "", "", err)
		default:
			return fail(http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to validate database", err)
		}
	}
	if db == nil || db.ID == "" {
		return fail(http.StatusInternalServerError, "INTERNAL_ERROR", "Database validation context is missing", nil)
	}
	allowed := db.OwnerID == principal.Subject
	for _, grant := range principal.DBAdmin {
		if grant == db.ID || (db.Slug != nil && *db.Slug != "" && grant == *db.Slug) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fail(http.StatusForbidden, "FORBIDDEN", "Full database access is required for replication", nil)
	}
	if expected != nil && db.ID != *expected {
		return fail(http.StatusConflict, "DATABASE_IDENTITY_MISMATCH", "Database identity does not match the bound database", nil)
	}
	return db, nil
}
