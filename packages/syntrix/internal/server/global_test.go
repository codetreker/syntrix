package server

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
)

type mockService struct {
	Service
}

func (m *mockService) RegisterHTTPHandler(pattern string, handler http.Handler)     {}
func (m *mockService) RegisterGRPCService(desc *grpc.ServiceDesc, impl interface{}) {}

func TestGlobal_SetDefault(t *testing.T) {
	done := make(chan bool)
	go func() {
		// Backup original default
		orig := Default()
		defer SetDefault(orig)

		// Test InitDefault
		cfg := Config{
			Host:     "localhost",
			HTTPPort: 8080,
			GRPCPort: 9000,
		}
		InitDefault(cfg, slog.Default())
		assert.NotNil(t, Default())

		// Test SetDefault
		ms := &mockService{}
		SetDefault(ms)
		assert.Equal(t, ms, Default())

		// Test Helper functions safety (checking for nil default)
		SetDefault(nil)
		assert.NotPanics(t, func() {
			RegisterHTTP("/test", http.NotFoundHandler())
			HandleFunc("/test2", func(w http.ResponseWriter, r *http.Request) {})
			RegisterGRPC(&grpc.ServiceDesc{}, nil)
		})

		// Test Helper functions with mock
		SetDefault(ms)
		assert.NotPanics(t, func() {
			RegisterHTTP("/test", http.NotFoundHandler())
			HandleFunc("/test2", func(w http.ResponseWriter, r *http.Request) {})
			RegisterGRPC(&grpc.ServiceDesc{}, nil)
		})
		done <- true
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("TestGlobal_SetDefault timed out")
	}
}
