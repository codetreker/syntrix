package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codetreker/syntrix/internal/core/identity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestHandleSignUp(t *testing.T) {
	mockAuth := new(MockAuthService)
	server := createTestServer(nil, mockAuth, nil)

	t.Run("Success", func(t *testing.T) {
		reqBody := identity.SignupRequest{Username: "newuser", Password: "password"}
		tokenPair := &identity.TokenPair{AccessToken: "access", RefreshToken: "refresh"}
		mockAuth.On("SignUp", mock.Anything, reqBody).Return(tokenPair, nil).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/signup", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleSignUp(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp identity.TokenPair
		json.NewDecoder(w.Body).Decode(&resp)
		assert.Equal(t, "access", resp.AccessToken)
	})

	t.Run("UserExists", func(t *testing.T) {
		reqBody := identity.SignupRequest{Username: "existing", Password: "password"}
		mockAuth.On("SignUp", mock.Anything, reqBody).Return(nil, errors.New("user already exists")).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/signup", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleSignUp(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("InvalidBody", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/auth/v1/signup", bytes.NewReader([]byte("invalid")))
		w := httptest.NewRecorder()

		server.handleSignUp(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestHandleLogin(t *testing.T) {
	mockAuth := new(MockAuthService)
	server := createTestServer(nil, mockAuth, nil)

	t.Run("Success", func(t *testing.T) {
		reqBody := identity.LoginRequest{Username: "user", Password: "password"}
		tokenPair := &identity.TokenPair{AccessToken: "access", RefreshToken: "refresh"}
		mockAuth.On("SignIn", mock.Anything, reqBody).Return(tokenPair, nil).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/login", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleLogin(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp identity.TokenPair
		json.NewDecoder(w.Body).Decode(&resp)
		assert.Equal(t, "access", resp.AccessToken)
	})

	t.Run("InvalidCredentials", func(t *testing.T) {
		reqBody := identity.LoginRequest{Username: "user", Password: "wrong"}
		mockAuth.On("SignIn", mock.Anything, reqBody).Return(nil, identity.ErrInvalidCredentials).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/login", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleLogin(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("AccountDisabled", func(t *testing.T) {
		reqBody := identity.LoginRequest{Username: "disabled", Password: "password"}
		mockAuth.On("SignIn", mock.Anything, reqBody).Return(nil, identity.ErrAccountDisabled).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/login", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleLogin(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("AccountLocked", func(t *testing.T) {
		reqBody := identity.LoginRequest{Username: "locked", Password: "password"}
		mockAuth.On("SignIn", mock.Anything, reqBody).Return(nil, identity.ErrAccountLocked).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/login", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleLogin(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("MissingUsername", func(t *testing.T) {
		reqBody := identity.LoginRequest{Username: "", Password: "password"}

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/login", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleLogin(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("InternalError", func(t *testing.T) {
		// Security: Internal errors should return 401 with generic message
		// to prevent information leakage about system state
		reqBody := identity.LoginRequest{Username: "user", Password: "password"}
		mockAuth.On("SignIn", mock.Anything, reqBody).Return(nil, errors.New("internal error")).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/login", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleLogin(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("InvalidBody", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/auth/v1/login", bytes.NewReader([]byte("invalid")))
		w := httptest.NewRecorder()

		server.handleLogin(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestHandleRefresh(t *testing.T) {
	mockAuth := new(MockAuthService)
	server := createTestServer(nil, mockAuth, nil)

	t.Run("Success", func(t *testing.T) {
		reqBody := identity.RefreshRequest{RefreshToken: "valid_refresh"}
		tokenPair := &identity.TokenPair{AccessToken: "new_access", RefreshToken: "new_refresh"}
		mockAuth.On("Refresh", mock.Anything, reqBody).Return(tokenPair, nil).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/refresh", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleRefresh(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		var resp identity.TokenPair
		json.NewDecoder(w.Body).Decode(&resp)
		assert.Equal(t, "new_access", resp.AccessToken)
	})

	t.Run("InvalidToken", func(t *testing.T) {
		reqBody := identity.RefreshRequest{RefreshToken: "invalid_refresh"}
		mockAuth.On("Refresh", mock.Anything, reqBody).Return(nil, errors.New("invalid token")).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/refresh", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleRefresh(w, req)

		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("MissingToken", func(t *testing.T) {
		reqBody := identity.RefreshRequest{RefreshToken: ""}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/refresh", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleRefresh(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestHandleLogout(t *testing.T) {
	mockAuth := new(MockAuthService)
	server := createTestServer(nil, mockAuth, nil)

	t.Run("Success_Body", func(t *testing.T) {
		reqBody := struct {
			RefreshToken string `json:"refresh_token"`
		}{RefreshToken: "refresh_token"}
		mockAuth.On("Logout", mock.Anything, "refresh_token").Return(nil).Once()

		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/logout", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleLogout(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("MissingToken", func(t *testing.T) {
		reqBody := struct {
			RefreshToken string `json:"refresh_token"`
		}{RefreshToken: ""}
		body, _ := json.Marshal(reqBody)
		req := httptest.NewRequest("POST", "/auth/v1/logout", bytes.NewReader(body))
		w := httptest.NewRecorder()

		server.handleLogout(w, req)

		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}
