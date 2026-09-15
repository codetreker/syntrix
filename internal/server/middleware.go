package server

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/server/ratelimit"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// contextKeyRequestID uses the unified context key for request ID
var contextKeyRequestID = ctxkeys.KeyRequestID

// APIError represents a structured error response
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes a structured JSON error response
func writeError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(APIError{Code: code, Message: message}); err != nil {
		slog.Warn("Failed to encode error response", "error", err)
	}
}

// GetRequestID retrieves the request ID from the context
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(contextKeyRequestID).(string); ok {
		return id
	}
	return ""
}

// --- HTTP Middleware ---

// Middleware defines a function that wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares in reverse order.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

func (s *serverImpl) wrapMiddleware(h http.Handler) http.Handler {
	mws := []Middleware{
		s.recoveryMiddleware,
		s.requestIDMiddleware,
		s.loggingMiddleware,
		s.securityHeadersMiddleware,
	}
	if s.cfg.EnableCORS {
		mws = append(mws, s.corsMiddleware)
	}
	// Add rate limiting if enabled
	if s.rateLimiter != nil {
		mws = append(mws, s.rateLimitMiddleware)
	}
	return Chain(h, mws...)
}

func (s *serverImpl) recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				s.logger.Error("Panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"error", err,
					"stack", string(debug.Stack()),
					"request_id", GetRequestID(r.Context()),
				)

				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *serverImpl) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = uuid.New().String()
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), contextKeyRequestID, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *serverImpl) loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(ww, r)

		duration := time.Since(start)
		reqID := GetRequestID(r.Context())

		level := slog.LevelInfo
		if ww.statusCode >= 500 {
			// Use WARN for client-initiated cancellations (499), ERROR for real errors
			if ww.statusCode == 499 || r.Context().Err() != nil {
				level = slog.LevelWarn
			} else {
				level = slog.LevelError
			}
		}

		s.logger.Log(r.Context(), level, "HTTP Request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.statusCode,
			"duration_ms", duration.Milliseconds(),
			"request_id", reqID,
			"ip", r.RemoteAddr,
		)
	})
}

// TimeoutMiddleware wraps a handler with a context timeout
func TimeoutMiddleware(timeout time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (s *serverImpl) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Check if origin is allowed
		allowedOrigin := ""
		if len(s.cfg.AllowedOrigins) == 0 {
			// No origins configured - allow all (development mode)
			allowedOrigin = origin
		} else {
			for _, o := range s.cfg.AllowedOrigins {
				if o == "*" {
					allowedOrigin = origin
					break
				}
				if o == origin {
					allowedOrigin = origin
					break
				}
			}
		}

		if allowedOrigin != "" && origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			if s.cfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(s.cfg.AllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(s.cfg.AllowedHeaders, ", "))
			w.Header().Set("Access-Control-Max-Age", strconv.Itoa(s.cfg.CORSMaxAge))
		}

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// securityHeadersMiddleware adds security-related HTTP headers to all responses.
func (s *serverImpl) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prevent MIME type sniffing
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Prevent clickjacking
		w.Header().Set("X-Frame-Options", "DENY")
		// XSS protection (legacy browsers)
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		// Referrer policy
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		// Content Security Policy - restrict to same origin
		w.Header().Set("Content-Security-Policy", "default-src 'self'")

		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware applies rate limiting based on client IP.
func (s *serverImpl) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ratelimit.GetClientIP(r)

		if !s.rateLimiter.Allow(key) {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(s.cfg.RateLimit.Window.Seconds()), 10))
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// responseWriter wraps http.ResponseWriter to capture status code
// It also implements http.Hijacker and http.Flusher to support WebSocket and SSE
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

// Unwrap lets ResponseController reach connection deadlines through logging.
func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// Hijack implements http.Hijacker interface for WebSocket support
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Flush implements http.Flusher interface for SSE support
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// --- gRPC Interceptors ---

func (s *serverImpl) unaryInterceptors() grpc.ServerOption {
	interceptors := []grpc.UnaryServerInterceptor{
		s.recoveryUnaryInterceptor,
		s.loggingUnaryInterceptor,
	}
	return grpc.ChainUnaryInterceptor(interceptors...)
}

func (s *serverImpl) streamInterceptors() grpc.ServerOption {
	interceptors := []grpc.StreamServerInterceptor{
		s.recoveryStreamInterceptor,
		s.loggingStreamInterceptor,
	}
	return grpc.ChainStreamInterceptor(interceptors...)
}

func (s *serverImpl) recoveryUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("gRPC panic recovered",
				"method", info.FullMethod,
				"error", r,
				"stack", string(debug.Stack()),
			)
			err = status.Errorf(codes.Internal, "Internal server error")
		}
	}()
	return handler(ctx, req)
}

func (s *serverImpl) recoveryStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("gRPC panic recovered",
				"method", info.FullMethod,
				"error", r,
				"stack", string(debug.Stack()),
			)
			err = status.Errorf(codes.Internal, "Internal server error")
		}
	}()
	return handler(srv, ss)
}

func (s *serverImpl) loggingUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	duration := time.Since(start)

	code := status.Code(err)
	level := slog.LevelInfo
	if code != codes.OK {
		// Use WARN for client-initiated cancellations and expected client errors
		// Use ERROR only for real server-side errors
		switch code {
		case codes.Canceled, codes.DeadlineExceeded:
			// Client cancelled the request
			level = slog.LevelWarn
		case codes.NotFound, codes.AlreadyExists, codes.InvalidArgument,
			codes.PermissionDenied, codes.Unauthenticated, codes.FailedPrecondition:
			// Expected client/business errors - not server errors
			level = slog.LevelInfo
		default:
			// Check if context was cancelled (might show up as Internal error)
			if ctx.Err() != nil {
				level = slog.LevelWarn
			} else {
				level = slog.LevelError
			}
		}
	}

	s.logger.Log(ctx, level, "gRPC Request",
		"method", info.FullMethod,
		"code", code,
		"duration", duration,
		"error", err,
	)

	return resp, err
}

func (s *serverImpl) loggingStreamInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	start := time.Now()
	err := handler(srv, ss)
	duration := time.Since(start)

	code := status.Code(err)
	level := slog.LevelInfo
	if code != codes.OK {
		level = slog.LevelError
	}

	s.logger.Log(ss.Context(), level, "gRPC Stream",
		"method", info.FullMethod,
		"code", code,
		"duration", duration,
		"error", err,
	)

	return err
}
