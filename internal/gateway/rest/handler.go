package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/query"
	"github.com/syntrixbase/syntrix/internal/server"
	"github.com/syntrixbase/syntrix/internal/server/ratelimit"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// contextKeyParsedBody uses the unified context key for parsed body
var contextKeyParsedBody = ctxkeys.KeyParsedBody

// getParsedBody retrieves the cached parsed body from the context.
// Returns nil if no cached body exists.
func getParsedBody(ctx context.Context) map[string]interface{} {
	if data, ok := ctx.Value(contextKeyParsedBody).(map[string]interface{}); ok {
		return data
	}
	return nil
}

type Handler struct {
	engine          query.Service
	auth            identity.AuthN
	authz           identity.AuthZ
	database        database.Service
	dbValidator     *DatabaseValidator
	authRateLimiter ratelimit.Limiter // Stricter rate limiter for auth endpoints
	authRLWindow    time.Duration     // Window for auth rate limiter (for Retry-After header)
}

// HandlerOption is a function that configures a Handler.
type HandlerOption func(*Handler)

// WithDatabaseService configures the handler with a database service.
func WithDatabaseService(svc database.Service) HandlerOption {
	return func(h *Handler) {
		h.database = svc
		if svc != nil {
			h.dbValidator = NewDatabaseValidator(svc)
		}
	}
}

// WithAuthRateLimiter configures the handler with a stricter rate limiter for auth endpoints.
func WithAuthRateLimiter(limiter ratelimit.Limiter, window time.Duration) HandlerOption {
	return func(h *Handler) {
		h.authRateLimiter = limiter
		h.authRLWindow = window
	}
}

// NewHandler creates a new Handler with required dependencies and optional configurations.
func NewHandler(engine query.Service, auth identity.AuthN, authz identity.AuthZ, opts ...HandlerOption) (*Handler, error) {
	if auth == nil {
		return nil, errors.New("authn service cannot be nil")
	}
	if authz == nil {
		return nil, errors.New("authz service cannot be nil")
	}

	h := &Handler{
		engine: engine,
		auth:   auth,
		authz:  authz,
	}

	for _, opt := range opts {
		opt(h)
	}

	return h, nil
}

// SetDatabaseService sets the database service for database management operations.
// Deprecated: Use WithDatabaseService option in NewHandler instead.
// This method is kept for backward compatibility.
func (h *Handler) SetDatabaseService(svc database.Service) {
	h.database = svc
	// Initialize database validator middleware
	if svc != nil {
		h.dbValidator = NewDatabaseValidator(svc)
	}
}

// SetAuthRateLimiter sets the stricter rate limiter for auth endpoints.
// Deprecated: Use WithAuthRateLimiter option in NewHandler instead.
// This method is kept for backward compatibility.
func (h *Handler) SetAuthRateLimiter(limiter ratelimit.Limiter, window time.Duration) {
	h.authRateLimiter = limiter
	h.authRLWindow = window
}

// withAuthRateLimit wraps a handler with auth-specific rate limiting.
// Uses stricter rate limits than general endpoints (e.g., 5/min vs 100/min).
func (h *Handler) withAuthRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.authRateLimiter == nil {
			// No auth rate limiter configured, pass through
			next(w, r)
			return
		}
		key := ratelimit.GetClientIP(r)
		if !h.authRateLimiter.Allow(key) {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(h.authRLWindow.Seconds()), 10))
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many authentication attempts")
			return
		}
		next(w, r)
	}
}

// withDatabaseValidation wraps a handler with database validation middleware.
// This validates that the database exists and is active before proceeding.
// Note: The validator check is done at request time, not at registration time,
// to allow SetDatabaseService to be called after RegisterRoutes.
func (h *Handler) withDatabaseValidation(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.dbValidator == nil {
			// No validator configured, pass through
			next(w, r)
			return
		}
		h.dbValidator.MiddlewareFunc(next)(w, r)
	}
}

// Default body size limits
const (
	DefaultMaxBodySize = 1 << 20  // 1MB
	LargeMaxBodySize   = 10 << 20 // 10MB for admin operations
)

// Default request timeout
const (
	DefaultRequestTimeout = 30 * time.Second
	LongRequestTimeout    = 60 * time.Second // For replication and admin operations
)

// APIError represents a structured error response
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes
const (
	ErrCodeBadRequest         = "BAD_REQUEST"
	ErrCodeUnauthorized       = "UNAUTHORIZED"
	ErrCodeForbidden          = "FORBIDDEN"
	ErrCodeNotFound           = "NOT_FOUND"
	ErrCodeConflict           = "CONFLICT"
	ErrCodePreconditionFailed = "PRECONDITION_FAILED"
	ErrCodeRequestTooLarge    = "REQUEST_TOO_LARGE"
	ErrCodeInternalError      = "INTERNAL_ERROR"
)

// writeError writes a structured JSON error response
func writeError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(APIError{Code: code, Message: message}); err != nil {
		slog.Warn("Failed to encode error response", "error", err)
	}
}

// writeInternalError writes an internal error response, but first checks if the error
// is due to client cancellation (returns 499 instead of 500).
// Use this for any error that would otherwise return 500 Internal Server Error.
func writeInternalError(w http.ResponseWriter, err error, message string) {
	if model.IsCanceled(err) {
		w.WriteHeader(499) // Client Closed Request
		return
	}
	slog.Error(message, "error", err)
	writeError(w, http.StatusInternalServerError, ErrCodeInternalError, message)
}

// writeStorageError writes an appropriate error response for storage errors
func writeStorageError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrNotFound):
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "Document not found")
	case errors.Is(err, model.ErrExists):
		writeError(w, http.StatusConflict, ErrCodeConflict, "Document already exists")
	case errors.Is(err, model.ErrPreconditionFailed):
		writeError(w, http.StatusPreconditionFailed, ErrCodePreconditionFailed, "Version conflict")
	default:
		writeInternalError(w, err, "Internal storage error")
	}
}

// writeJSON writes a JSON response with proper error handling
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		slog.Warn("Failed to encode JSON response", "error", err)
	}
}

// logRequest logs request information using structured logging
func logRequest(r *http.Request, status int, duration time.Duration) {
	slog.Info("HTTP request",
		"method", r.Method,
		"path", r.URL.Path,
		"status", status,
		"duration_ms", duration.Milliseconds(),
		"request_id", server.GetRequestID(r.Context()),
	)
}

// maxBodySize wraps a handler with request body size limiting
func maxBodySize(next http.HandlerFunc, maxBytes int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		}
		next(w, r)
	}
}

// withTimeout wraps a handler with a context timeout
// If the handler takes longer than the timeout, the context is cancelled
func withTimeout(next http.HandlerFunc, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next(w, r.WithContext(ctx))
	}
}

func (h *Handler) getDatabase(r *http.Request) (string, error) {
	// Extract database from URL path (e.g., /api/v1/databases/{database}/documents/...)
	if database := r.PathValue("database"); database != "" {
		return database, nil
	}

	return "", errors.New("database is required in URL path")
}

func (h *Handler) databaseOrError(w http.ResponseWriter, r *http.Request) (string, bool) {
	database, err := h.getDatabase(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Database identification required")
		return "", false
	}
	return database, true
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Document Operations (with body size limit for write operations)
	// Note: Request ID and panic recovery are handled by the unified server middleware
	// URL format: /api/v1/databases/{database}/documents/{path...}
	// Database validation middleware ensures database exists and is active
	mux.HandleFunc("GET /api/v1/databases/{database}/documents/{path...}", withTimeout(h.withDatabaseValidation(h.maybeProtected(h.authorized(h.handleGetDocument, "read"))), DefaultRequestTimeout))
	mux.HandleFunc("POST /api/v1/databases/{database}/documents/{path...}", withTimeout(maxBodySize(h.withDatabaseValidation(h.maybeProtected(h.authorized(h.handleCreateDocument, "create"))), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("PUT /api/v1/databases/{database}/documents/{path...}", withTimeout(maxBodySize(h.withDatabaseValidation(h.maybeProtected(h.authorized(h.handleReplaceDocument, "update"))), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("PATCH /api/v1/databases/{database}/documents/{path...}", withTimeout(maxBodySize(h.withDatabaseValidation(h.maybeProtected(h.authorized(h.handlePatchDocument, "update"))), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("DELETE /api/v1/databases/{database}/documents/{path...}", withTimeout(maxBodySize(h.withDatabaseValidation(h.maybeProtected(h.authorized(h.handleDeleteDocument, "delete"))), DefaultMaxBodySize), DefaultRequestTimeout))

	// Query Operations
	// URL format: /api/v1/databases/{database}/query
	mux.HandleFunc("POST /api/v1/databases/{database}/query", withTimeout(maxBodySize(h.withDatabaseValidation(h.protected(h.handleQuery)), DefaultMaxBodySize), DefaultRequestTimeout))

	// Replication Operations (use longer timeout for potentially large data transfers)
	// URL format: /replication/v1/databases/{database}/pull
	mux.HandleFunc("POST /replication/v1/databases/{database}/pull", withTimeout(h.protected(h.withDatabaseValidation(h.pullAuthorized(h.handlePull))), DefaultRequestTimeout))
	mux.HandleFunc("POST /replication/v1/databases/{database}/push", withTimeout(maxBodySize(h.withDatabaseValidation(h.protected(h.handlePush)), LargeMaxBodySize), LongRequestTimeout))

	// Trigger Internal Operations
	// URL format: /trigger/v1/databases/{database}/get
	mux.HandleFunc("POST /trigger/v1/databases/{database}/get", withTimeout(maxBodySize(h.withDatabaseValidation(h.triggerProtected(h.handleTriggerGet)), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("POST /trigger/v1/databases/{database}/query", withTimeout(maxBodySize(h.withDatabaseValidation(h.triggerProtected(h.handleQuery)), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("POST /trigger/v1/databases/{database}/write", withTimeout(maxBodySize(h.withDatabaseValidation(h.triggerProtected(h.handleTriggerWrite)), DefaultMaxBodySize), DefaultRequestTimeout))

	// Auth Operations (with stricter rate limiting)
	// Auth rate limiting is applied in addition to general rate limiting
	mux.HandleFunc("POST /auth/v1/signup", withTimeout(maxBodySize(h.withAuthRateLimit(h.handleSignUp), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("POST /auth/v1/login", withTimeout(maxBodySize(h.withAuthRateLimit(h.handleLogin), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("POST /auth/v1/refresh", withTimeout(maxBodySize(h.withAuthRateLimit(h.handleRefresh), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("POST /auth/v1/logout", withTimeout(maxBodySize(h.handleLogout, DefaultMaxBodySize), DefaultRequestTimeout))

	// Admin Operations (use longer timeout)
	mux.HandleFunc("GET /admin/users", withTimeout(h.adminOnly(h.handleAdminListUsers), LongRequestTimeout))
	mux.HandleFunc("PATCH /admin/users/{id}", withTimeout(maxBodySize(h.adminOnly(h.handleAdminUpdateUser), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("GET /admin/rules", withTimeout(h.adminOnly(h.handleAdminGetRules), DefaultRequestTimeout))
	mux.HandleFunc("POST /admin/rules/push", withTimeout(maxBodySize(h.adminOnly(h.handleAdminPushRules), LargeMaxBodySize), LongRequestTimeout))
	mux.HandleFunc("GET /admin/health", withTimeout(h.adminOnly(h.handleAdminHealth), DefaultRequestTimeout))

	// Database Management - Admin API
	mux.HandleFunc("POST /admin/databases", withTimeout(maxBodySize(h.adminOnly(h.handleAdminCreateDatabase), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("GET /admin/databases", withTimeout(h.adminOnly(h.handleAdminListDatabases), LongRequestTimeout))
	mux.HandleFunc("GET /admin/databases/{identifier}", withTimeout(h.adminOnly(h.handleAdminGetDatabase), DefaultRequestTimeout))
	mux.HandleFunc("PATCH /admin/databases/{identifier}", withTimeout(maxBodySize(h.adminOnly(h.handleAdminUpdateDatabase), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("DELETE /admin/databases/{identifier}", withTimeout(h.adminOnly(h.handleAdminDeleteDatabase), DefaultRequestTimeout))

	// Database Management - User API
	// URL format: /api/v1/databases
	mux.HandleFunc("POST /api/v1/databases", withTimeout(maxBodySize(h.protected(h.handleCreateDatabase), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("GET /api/v1/databases", withTimeout(h.protected(h.handleListDatabases), DefaultRequestTimeout))
	mux.HandleFunc("GET /api/v1/databases/{identifier}", withTimeout(h.protected(h.handleGetDatabase), DefaultRequestTimeout))
	mux.HandleFunc("PATCH /api/v1/databases/{identifier}", withTimeout(maxBodySize(h.protected(h.handleUpdateDatabase), DefaultMaxBodySize), DefaultRequestTimeout))
	mux.HandleFunc("DELETE /api/v1/databases/{identifier}", withTimeout(h.protected(h.handleDeleteDatabase), DefaultRequestTimeout))

	// Health Check (no auth, minimal timeout)
	mux.HandleFunc("GET /health", withTimeout(h.handleHealth, 5*time.Second))
}

func (h *Handler) protected(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.auth.Middleware(handler).ServeHTTP(w, r)
	}
}

func (h *Handler) maybeProtected(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.auth.MiddlewareOptional(handler).ServeHTTP(w, r)
	}
}

func (h *Handler) authorized(handler http.HandlerFunc, action string) http.HandlerFunc {
	// AuthZ is guaranteed to be non-nil (panic in NewHandler if nil)
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")

		// Extract database from URL path
		databaseID := r.PathValue("database")
		if databaseID == "" {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Database is required")
			return
		}

		// Build Request Context
		reqCtx := identity.AuthzRequest{
			Time: time.Now(),
		}

		// Extract Auth
		if uid, ok := r.Context().Value(identity.ContextKeyUserID).(string); ok {
			reqCtx.Auth.UID = uid
		}
		if username, ok := r.Context().Value(identity.ContextKeyUsername).(string); ok {
			reqCtx.Auth.Username = username
		}
		if roles, ok := r.Context().Value(identity.ContextKeyRoles).([]string); ok {
			reqCtx.Auth.Roles = append([]string{}, roles...)
		}
		if dbAdmin, ok := r.Context().Value(identity.ContextKeyDBAdmin).([]string); ok {
			reqCtx.Auth.DBAdmin = append([]string{}, dbAdmin...)
		}
		if claims, ok := r.Context().Value(identity.ContextKeyClaims).(*identity.Claims); ok {
			reqCtx.Auth.Claims = claimsToMap(claims)
		}

		// Owner implicit db_admin: if database owner matches user ID, add to DBAdmin list
		if db, ok := database.FromContext(r.Context()); ok {
			if db.OwnerID == reqCtx.Auth.UID && reqCtx.Auth.UID != "" {
				// Check if database ID/slug is already in DBAdmin list
				found := false
				for _, admin := range reqCtx.Auth.DBAdmin {
					if admin == db.ID || (db.Slug != nil && admin == *db.Slug) {
						found = true
						break
					}
				}
				if !found {
					// Add database ID to DBAdmin list (use ID for consistency)
					reqCtx.Auth.DBAdmin = append(reqCtx.Auth.DBAdmin, db.ID)
				}
			}
		}

		// Fetch Existing Resource if needed
		var existingRes *identity.Resource
		if action != "create" {
			doc, err := h.engine.GetDocument(r.Context(), databaseID, path)
			if err == nil {
				data := model.Document{}
				for k, v := range doc {
					data[k] = v
				}
				data.StripProtectedFields()
				existingRes = &identity.Resource{
					Data: data,
					ID:   doc.GetID(),
				}
			} else if err != model.ErrNotFound {
				writeInternalError(w, err, "Failed to check resource")
				return
			}
		}

		// For Create/Update, extract new data and cache parsed body
		if action == "create" || action == "update" {
			bodyBytes, err := io.ReadAll(r.Body)
			if err != nil {
				writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Failed to read request body")
				return
			}
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			var data map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &data); err == nil {
				reqCtx.Resource = &identity.Resource{Data: data}
				// Cache parsed body in context to avoid double parsing in handlers
				ctx := context.WithValue(r.Context(), contextKeyParsedBody, data)
				r = r.WithContext(ctx)
			}
			// Note: If JSON parsing fails here, we don't cache and let the handler
			// report the error with more specific context
		}

		allowed, err := h.authz.Evaluate(r.Context(), databaseID, path, action, reqCtx, existingRes)
		if err != nil {
			slog.Warn("Authorization rule evaluation error",
				"database", databaseID,
				"path", path,
				"action", action,
				"error", err,
				"request_id", server.GetRequestID(r.Context()),
			)
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "Authorization check failed")
			return
		}

		if !allowed {
			writeError(w, http.StatusForbidden, ErrCodeForbidden, "Access denied")
			return
		}

		handler(w, r)
	}
}

func claimsToMap(claims *identity.Claims) map[string]interface{} {
	if claims == nil {
		return nil
	}

	toTime := func(nd *jwt.NumericDate) interface{} {
		if nd == nil {
			return nil
		}
		return nd.Time
	}

	return map[string]interface{}{
		"sub":      claims.Subject,
		"db_admin": claims.DBAdmin,
		"oid":      claims.UserID,
		"username": claims.Username,
		"roles":    append([]string{}, claims.Roles...),
		"disabled": claims.Disabled,
		"aud":      claims.Audience,
		"iss":      claims.Issuer,
		"jti":      claims.ID,
		"nbf":      toTime(claims.NotBefore),
		"exp":      toTime(claims.ExpiresAt),
		"iat":      toTime(claims.IssuedAt),
	}
}

func (h *Handler) triggerProtected(handler http.HandlerFunc) http.HandlerFunc {
	// Auth is guaranteed to be non-nil (panic in NewHandler if nil)
	return func(w http.ResponseWriter, r *http.Request) {
		// First, run standard auth middleware to validate token
		h.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check roles
			roles, ok := r.Context().Value(identity.ContextKeyRoles).([]string)
			if !ok {
				writeError(w, http.StatusForbidden, ErrCodeForbidden, "Access denied")
				return
			}

			isSystem := false
			for _, role := range roles {
				if role == "system" {
					isSystem = true
					break
				}
			}

			if !isSystem {
				writeError(w, http.StatusForbidden, ErrCodeForbidden, "System access required")
				return
			}

			handler(w, r)
		})).ServeHTTP(w, r)
	}
}

func (h *Handler) adminOnly(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// First, run standard auth middleware to validate token
		h.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check roles
			roles, ok := r.Context().Value(identity.ContextKeyRoles).([]string)
			if !ok {
				writeError(w, http.StatusForbidden, ErrCodeForbidden, "Access denied")
				return
			}

			isAdmin := false
			for _, role := range roles {
				if role == "admin" || role == "system" {
					isAdmin = true
					break
				}
			}

			if !isAdmin {
				writeError(w, http.StatusForbidden, ErrCodeForbidden, "Admin access required")
				return
			}

			handler(w, r)
		})).ServeHTTP(w, r)
	}
}
