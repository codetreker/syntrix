package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/codetreker/syntrix/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeHTTP_OptionsSetsCORS(t *testing.T) {
	server := createTestServer(nil, nil, nil)

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, w.Header().Get("Access-Control-Allow-Methods"), "OPTIONS")
}

func TestServeHTTP_CORSHeadersOnGET(t *testing.T) {
	server := createTestServer(nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	server.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestProtectedAuthentication(t *testing.T) {
	for _, optional := range []bool{false, true} {
		for _, header := range []string{"", "Bearer good", "Bearer bad"} {
			auth := new(MockAuthService)
			if header == "Bearer bad" {
				auth.On("VerifyToken", "bad").Return(nil, identity.ErrInvalidToken).Once()
			}
			server, err := NewHandler(nil, auth, auth, new(AllowAllAuthzService))
			require.NoError(t, err)
			next := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusCreated) }
			handler := server.protected(next)
			if optional {
				handler = server.maybeProtected(next)
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", header)
			response := httptest.NewRecorder()
			handler(response, request)
			want := http.StatusCreated
			if header == "Bearer bad" || (!optional && header == "") {
				want = http.StatusUnauthorized
			}
			assert.Equal(t, want, response.Code)
			auth.AssertExpectations(t)
		}
	}
}
