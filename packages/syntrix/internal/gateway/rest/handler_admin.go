package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/codetreker/syntrix/internal/gateway/authentication"
	"github.com/codetreker/syntrix/internal/identity"
)

func (h *Handler) handleAdminListUsers(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	offset := 0

	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			limit = l
		}
	}
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			offset = o
		}
	}

	users, err := h.auth.ListUsers(r.Context(), authentication.FromContext(r.Context()), limit, offset)
	if err != nil {
		writeInternalError(w, err, "Failed to list users")
		return
	}

	var response []*userResponse
	if users != nil {
		response = make([]*userResponse, len(users))
		for i, user := range users {
			response[i] = &userResponse{User: user}
		}
	}
	writeJSON(w, http.StatusOK, response)
}

type userResponse struct {
	*identity.User
	PasswordHash string `json:"password_hash"`
	PasswordAlgo string `json:"password_algo"`
}

type UpdateUserRequest struct {
	Roles    []string `json:"roles"`
	DBAdmin  []string `json:"db_admin"`
	Disabled bool     `json:"disabled"`
}

func (h *Handler) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Missing user ID")
		return
	}

	var req UpdateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	if err := h.auth.UpdateUser(r.Context(), authentication.FromContext(r.Context()), id, req.Roles, req.DBAdmin, req.Disabled); err != nil {
		writeInternalError(w, err, "Failed to update user")
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleAdminGetRules(w http.ResponseWriter, r *http.Request) {
	database := r.URL.Query().Get("database")
	if database == "" {
		database = "default"
	}
	rules := h.authz.GetRulesForDatabase(database)
	if rules == nil {
		writeError(w, http.StatusNotFound, ErrCodeNotFound, "No rules found for database")
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

func (h *Handler) handleAdminPushRules(w http.ResponseWriter, r *http.Request) {
	database := r.URL.Query().Get("database")
	if database == "" {
		database = "default"
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Failed to read request body")
		return
	}

	if err := h.authz.UpdateRules(database, body); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid rules format: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}
