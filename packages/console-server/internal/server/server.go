package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/auth"
	"github.com/codetreker/syntrix/console-server/internal/server/config"
	"github.com/codetreker/syntrix/console-server/internal/workload"
)

type Accounts interface {
	Login(context.Context, string, string) (auth.Session, string, error)
	Authenticate(context.Context, string) (auth.Session, error)
	Logout(context.Context, string) error
}

type Workloads interface {
	Create(context.Context, string, string, string) (workload.Workload, error)
	List(context.Context, string) ([]workload.Workload, error)
	Get(context.Context, string, string) (workload.Workload, error)
	Delete(context.Context, string, string) (workload.Workload, error)
}

type Server struct {
	cfg       config.Config
	accounts  Accounts
	workloads Workloads
	templates []string
	logger    *slog.Logger
	ready     func(context.Context) error
}

func New(cfg config.Config, accounts Accounts, workloads Workloads, templates []string, logger *slog.Logger, ready func(context.Context) error) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if accounts == nil || workloads == nil || logger == nil || ready == nil {
		return nil, errors.New("Console HTTP dependencies are required")
	}
	return &Server{cfg: cfg, accounts: accounts, workloads: workloads, templates: append([]string(nil), templates...), logger: logger, ready: ready}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := s.ready(ctx); err != nil {
			s.failure(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /auth/v1/login", s.login)
	mux.HandleFunc("GET /auth/v1/me", s.me)
	mux.HandleFunc("POST /auth/v1/logout", s.logout)
	mux.HandleFunc("GET /api/v1/templates", s.listTemplates)
	mux.HandleFunc("POST /api/v1/workloads", s.create)
	mux.HandleFunc("GET /api/v1/workloads", s.list)
	mux.HandleFunc("GET /api/v1/workloads/{id}", s.get)
	mux.HandleFunc("DELETE /api/v1/workloads/{id}", s.delete)
	protection := http.NewCrossOriginProtection()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var id [16]byte
		if _, err := rand.Read(id[:]); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "request initialization failed"})
			return
		}
		requestID := hex.EncodeToString(id[:])
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		requestContext, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
		defer cancel()
		ctx := context.WithValue(requestContext, requestIDKey{}, requestID)
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("Origin") != s.cfg.PublicOrigin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "trusted Origin is required"})
			return
		}
		protection.Handler(mux).ServeHTTP(w, r.WithContext(ctx))
	})
}

type requestIDKey struct{}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	session, token, err := s.accounts.Login(r.Context(), body.Username, body.Password)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	s.setCookie(w, token, session.ExpiresAt)
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request, mutation bool) (auth.Session, string, bool) {
	cookie, err := r.Cookie("console_session")
	if err != nil {
		s.failure(w, r, auth.ErrUnauthorized)
		return auth.Session{}, "", false
	}
	session, err := s.accounts.Authenticate(r.Context(), cookie.Value)
	if err != nil {
		s.failure(w, r, err)
		return auth.Session{}, "", false
	}
	if mutation {
		token := r.Header.Get("X-CSRF-Token")
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(session.CSRFToken)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid CSRF token"})
			return auth.Session{}, "", false
		}
	}
	return session, cookie.Value, true
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.session(w, r, false)
	if ok {
		writeJSON(w, http.StatusOK, session)
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_, token, ok := s.session(w, r, true)
	if !ok {
		return
	}
	if err := s.accounts.Logout(r.Context(), token); err != nil {
		s.failure(w, r, err)
		return
	}
	s.setCookie(w, "", time.Unix(1, 0))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.session(w, r, false); !ok {
		return
	}
	result := make([]map[string]string, 0, len(s.templates))
	for _, id := range s.templates {
		result = append(result, map[string]string{"id": id})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.session(w, r, true)
	if !ok {
		return
	}
	var body struct {
		TemplateID string `json:"template_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	item, err := s.workloads.Create(r.Context(), session.Developer.ID, body.TemplateID, r.Header.Get("Idempotency-Key"))
	if err != nil {
		s.failure(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/workloads/"+item.ID)
	writeJSON(w, http.StatusAccepted, item)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.session(w, r, false)
	if !ok {
		return
	}
	items, err := s.workloads.List(r.Context(), session.Developer.ID)
	if err != nil {
		s.failure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.session(w, r, false)
	if !ok {
		return
	}
	item, err := s.workloads.Get(r.Context(), session.Developer.ID, r.PathValue("id"))
	if err != nil {
		s.failure(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.session(w, r, true)
	if !ok {
		return
	}
	item, err := s.workloads.Delete(r.Context(), session.Developer.ID, r.PathValue("id"))
	if err != nil {
		s.failure(w, r, err)
		return
	}
	status := http.StatusAccepted
	if item.Status == "deleted" {
		status = http.StatusOK
	}
	writeJSON(w, status, item)
}

func (s *Server) setCookie(w http.ResponseWriter, token string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if token == "" {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: "console_session", Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.cfg.PublicOrigin, "https://"), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: maxAge})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON request"})
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request must contain one JSON value"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) failure(w http.ResponseWriter, r *http.Request, err error) {
	status, message := http.StatusServiceUnavailable, "operation unavailable"
	switch {
	case errors.Is(err, auth.ErrUnauthorized):
		status, message = http.StatusUnauthorized, "authentication required"
	case errors.Is(err, workload.ErrNotFound):
		status, message = http.StatusNotFound, "workload not found"
	case errors.Is(err, workload.ErrConflict):
		status, message = http.StatusConflict, "request conflicts with existing operation"
	case errors.Is(err, workload.ErrInvalid):
		status, message = http.StatusBadRequest, "invalid workload request"
	default:
		cause := err
		for errors.Unwrap(cause) != nil {
			cause = errors.Unwrap(cause)
		}
		fields := []any{"request_id", r.Context().Value(requestIDKey{}), "phase", r.Pattern, "error_category", "dependency", "error_type", fmt.Sprintf("%T", cause)}
		var sqlError interface{ SQLState() string }
		if errors.As(err, &sqlError) {
			fields = append(fields, "sql_state", sqlError.SQLState())
		}
		s.logger.ErrorContext(r.Context(), "Console request failed", fields...)
	}
	writeJSON(w, status, map[string]string{"error": message})
}
