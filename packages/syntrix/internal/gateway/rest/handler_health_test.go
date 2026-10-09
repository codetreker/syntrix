package rest

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHandleHealth(t *testing.T) {
	mockService := new(MockQueryService)
	server := createTestServer(mockService, nil, nil)

	req, _ := http.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()

	server.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "OK", rr.Body.String())
}
