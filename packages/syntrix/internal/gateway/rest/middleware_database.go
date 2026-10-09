package rest

import (
	"errors"
	"net/http"

	"github.com/codetreker/syntrix/internal/core/database"
)

// DatabaseValidator provides middleware for validating database access
type DatabaseValidator struct {
	service database.Service
}

// NewDatabaseValidator creates a new database validator middleware
func NewDatabaseValidator(service database.Service) *DatabaseValidator {
	return &DatabaseValidator{service: service}
}

// Middleware returns a middleware handler that validates database existence and status.
// It extracts the database identifier from the URL path, resolves it, and stores
// the database in the request context for downstream handlers.
func (v *DatabaseValidator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identifier := r.PathValue("database")
		if identifier == "" {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest,
				"Database identifier is required in URL path")
			return
		}

		// Resolve identifier to database (checks existence and status)
		db, err := v.service.ResolveDatabase(r.Context(), identifier)
		if err != nil {
			v.writeDatabaseValidationError(w, identifier, err)
			return
		}

		// Store resolved database in context for downstream handlers
		ctx := database.WithDatabase(r.Context(), db)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// MiddlewareFunc is a convenience wrapper that returns http.HandlerFunc
func (v *DatabaseValidator) MiddlewareFunc(next http.HandlerFunc) http.HandlerFunc {
	return v.Middleware(http.HandlerFunc(next)).ServeHTTP
}

// writeDatabaseValidationError writes an appropriate error response for database validation errors
func (v *DatabaseValidator) writeDatabaseValidationError(w http.ResponseWriter, identifier string, err error) {
	switch {
	case errors.Is(err, database.ErrDatabaseNotFound):
		writeError(w, http.StatusNotFound, ErrCodeDatabaseNotFound,
			"Database '"+identifier+"' does not exist")
	case errors.Is(err, database.ErrDatabaseSuspended):
		writeError(w, http.StatusForbidden, ErrCodeDatabaseSuspended,
			"Database '"+identifier+"' is suspended")
	case errors.Is(err, database.ErrDatabaseDeleting):
		writeError(w, http.StatusGone, ErrCodeDatabaseDeleting,
			"Database '"+identifier+"' is being deleted")
	default:
		writeInternalError(w, err, "Failed to validate database")
	}
}
