package ratelimit

import (
	"net"
	"net/http"
	"strings"
)

// Middleware returns an HTTP middleware that applies rate limiting.
// The key function extracts the rate limit key from the request (typically client IP).
func Middleware(limiter Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := GetClientIP(r)

			if !limiter.Allow(key) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// GetClientIP extracts the client IP address from the request.
// It checks X-Forwarded-For header first (for proxied requests),
// then X-Real-IP, and finally falls back to RemoteAddr.
func GetClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (set by proxies)
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Take the first IP (original client)
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
