package rest

import (
	"log/slog"
	"net/http"

	"github.com/codetreker/syntrix/internal/core/identity"
	"github.com/codetreker/syntrix/pkg/model"
)

// Generic authentication error message to prevent account enumeration
const authFailedMessage = "Invalid credentials"

func (h *Handler) handleSignUp(w http.ResponseWriter, r *http.Request) {
	req, err := decodeAndValidate[identity.SignupRequest](r)
	if err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"code":    ErrCodeBadRequest,
				"message": "Validation failed",
				"errors":  ve.Errors,
			})
			return
		}
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	tokenPair, err := h.auth.SignUp(r.Context(), *req)
	if err != nil {
		// Log the error for debugging (using Debug level to avoid log noise in production)
		slog.Debug("SignUp failed", "username", req.Username, "error", err.Error())
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Signup failed")
		return
	}

	writeJSON(w, http.StatusOK, tokenPair)
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	req, err := decodeAndValidate[identity.LoginRequest](r)
	if err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"code":    ErrCodeBadRequest,
				"message": "Validation failed",
				"errors":  ve.Errors,
			})
			return
		}
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	tokenPair, err := h.auth.SignIn(r.Context(), *req)
	if err != nil {
		// Log the specific error internally for debugging, but return generic message to client
		// This prevents account enumeration attacks
		slog.Debug("Authentication failed",
			"username", req.Username,
			"error", err.Error(),
		)

		if model.IsCanceled(err) {
			w.WriteHeader(499)
			return
		}

		// Return generic error message for all authentication failures
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, authFailedMessage)
		return
	}

	writeJSON(w, http.StatusOK, tokenPair)
}

func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	req, err := decodeAndValidate[identity.RefreshRequest](r)
	if err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"code":    ErrCodeBadRequest,
				"message": "Validation failed",
				"errors":  ve.Errors,
			})
			return
		}
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	tokenPair, err := h.auth.Refresh(r.Context(), *req)
	if err != nil {
		writeError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "Invalid or expired refresh token")
		return
	}

	writeJSON(w, http.StatusOK, tokenPair)
}

// LogoutRequest represents the logout payload.
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	req, err := decodeAndValidate[LogoutRequest](r)
	if err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"code":    ErrCodeBadRequest,
				"message": "Validation failed",
				"errors":  ve.Errors,
			})
			return
		}
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	if err := h.auth.Logout(r.Context(), req.RefreshToken); err != nil {
		writeInternalError(w, err, "Logout failed")
		return
	}

	w.WriteHeader(http.StatusOK)
}
