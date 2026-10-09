package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/identity"
	"github.com/codetreker/syntrix/internal/trigger/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type MockAuthN struct {
	mock.Mock
}

func (m *MockAuthN) Middleware(next http.Handler) http.Handler         { return next }
func (m *MockAuthN) MiddlewareOptional(next http.Handler) http.Handler { return next }
func (m *MockAuthN) SignIn(ctx context.Context, req identity.LoginRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthN) SignUp(ctx context.Context, req identity.SignupRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthN) Refresh(ctx context.Context, req identity.RefreshRequest) (*identity.TokenPair, error) {
	return nil, nil
}
func (m *MockAuthN) ListUsers(ctx context.Context, limit int, offset int) ([]*identity.User, error) {
	return nil, nil
}
func (m *MockAuthN) UpdateUser(ctx context.Context, id string, roles []string, dbAdmin []string, disabled bool) error {
	return nil
}
func (m *MockAuthN) Logout(ctx context.Context, refreshToken string) error { return nil }
func (m *MockAuthN) GenerateSystemToken(serviceName string) (string, error) {
	args := m.Called(serviceName)
	return args.String(0), args.Error(1)
}
func (m *MockAuthN) ValidateToken(tokenString string) (*identity.Claims, error) { return nil, nil }

func TestDeliveryWorker_ProcessTask(t *testing.T) {
	// 1. Setup Mock Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Method
		assert.Equal(t, "POST", r.Method)

		// Verify Headers
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Syntrix-Trigger-Service/1.0", r.Header.Get("User-Agent"))
		assert.Equal(t, "bar", r.Header.Get("X-Custom-Header"))
		// No signature when SecretsRef is empty
		assert.Empty(t, r.Header.Get("X-Syntrix-Signature"))

		// Verify Body
		var task types.DeliveryTask
		err := json.NewDecoder(r.Body).Decode(&task)
		assert.NoError(t, err)
		assert.Equal(t, "trig-1", task.TriggerID)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// 2. Setup Worker
	worker := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)

	// 3. Create Task (no SecretsRef, so no signature)
	task := &types.DeliveryTask{
		TriggerID: "trig-1",
		URL:       server.URL,
		Headers:   map[string]string{"X-Custom-Header": "bar"},
		Event:     "create",
	}

	// 4. Execute
	err := worker.ProcessTask(context.Background(), task)
	assert.NoError(t, err)
}

func TestDeliveryWorker_ProcessTask_Failure(t *testing.T) {
	// 1. Setup Mock Server that fails
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	worker := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)

	task := &types.DeliveryTask{
		TriggerID: "trig-1",
		URL:       server.URL,
	}

	err := worker.ProcessTask(context.Background(), task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "webhook failed with status: 500")
	assert.False(t, types.IsFatal(err))
}

func TestDeliveryWorker_ProcessTask_Timeouts(t *testing.T) {
	for _, tt := range []struct {
		name          string
		contextLimit  time.Duration
		clientLimit   time.Duration
		wantParentErr error
	}{
		{name: "task_context", contextLimit: 100 * time.Millisecond, wantParentErr: context.DeadlineExceeded},
		{name: "explicit_client_cap", contextLimit: 5 * time.Second, clientLimit: 100 * time.Millisecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), tt.contextLimit)
			defer cancel()
			w := NewDeliveryWorker(nil, nil, HTTPClientOptions{Timeout: tt.clientLimit}, nil)

			err := w.ProcessTask(ctx, &types.DeliveryTask{URL: server.URL})
			assert.ErrorIs(t, err, context.DeadlineExceeded)
			assert.False(t, types.IsFatal(err))
			assert.Equal(t, tt.wantParentErr, ctx.Err())
		})
	}
}

func TestDeliveryWorker_ProcessTask_BeyondFormerHTTPTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		// Six seconds crosses the former implicit five-second HTTP limit.
		timer := time.NewTimer(6 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			w.WriteHeader(http.StatusNoContent)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	w := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)

	require.NoError(t, w.ProcessTask(ctx, &types.DeliveryTask{URL: server.URL}))
}

func TestDeliveryWorker_ProcessTask_SecretCancellation(t *testing.T) {
	secrets := &MockSecretProvider{getSecret: func(ctx context.Context, ref string) (string, error) {
		assert.Equal(t, "signing-key", ref)
		<-ctx.Done()
		return "", ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	w := NewDeliveryWorker(nil, secrets, HTTPClientOptions{}, nil)

	err := w.ProcessTask(ctx, &types.DeliveryTask{URL: "http://localhost/webhook", SecretsRef: "signing-key"})
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorContains(t, err, "failed to resolve secret signing-key")
	assert.False(t, types.IsFatal(err))
}

func TestDeliveryWorker_ProcessTask_FatalError(t *testing.T) {
	// 1. Setup Mock Server that returns 400
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	worker := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)

	task := &types.DeliveryTask{
		TriggerID: "trig-1",
		URL:       server.URL,
	}

	err := worker.ProcessTask(context.Background(), task)
	assert.Error(t, err)
	assert.True(t, types.IsFatal(err))
	assert.Contains(t, err.Error(), "webhook failed with status: 400")
}

func TestDeliveryWorker_ProcessTask_RateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)
	err := w.ProcessTask(ctx, &types.DeliveryTask{
		TriggerID: "rate-limited-trigger",
		URL:       server.URL,
	})

	assert.ErrorContains(t, err, "webhook failed with status: 429")
	assert.False(t, types.IsFatal(err))
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "missing"},
		{name: "seconds", value: "60", want: time.Minute},
		{name: "leading_zeros", value: "0060", want: time.Minute},
		{name: "whitespace", value: " \t60\t ", want: time.Minute},
		{name: "zero", value: "0"},
		{name: "negative", value: "-60"},
		{name: "positive_sign", value: "+60"},
		{name: "decimal", value: "1.5"},
		{name: "duration_unit", value: "60s"},
		{name: "malformed", value: "later"},
		{name: "multiple_seconds", value: "60, 120"},
		{name: "http_date", value: future.Format(http.TimeFormat), want: time.Minute},
		{name: "rfc850_date", value: future.Format("Monday, 02-Jan-06 15:04:05 GMT"), want: time.Minute},
		{name: "asctime_date", value: future.Format(time.ANSIC), want: time.Minute},
		{name: "past_date", value: now.Add(-time.Minute).Format(http.TimeFormat)},
		{name: "current_date", value: now.Format(http.TimeFormat)},
		{name: "date_and_seconds", value: future.Format(http.TimeFormat) + ", 60"},
		{name: "multiple_dates", value: future.Format(http.TimeFormat) + ", " + future.Format(http.TimeFormat)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay, err := parseRetryAfter(tt.value, now)
			require.NoError(t, err)
			assert.Equal(t, tt.want, delay)
		})
	}
}

func TestDeliveryWorker_ProcessTask_RetryAfter(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		want    time.Duration
		fatal   bool
	}{
		{name: "seconds", headers: []string{"60"}, want: time.Minute},
		{name: "missing"},
		{name: "invalid", headers: []string{"later"}},
		{name: "zero", headers: []string{"0"}},
		{name: "past_date", headers: []string{"Sun, 06 Nov 1994 08:49:37 GMT"}},
		{name: "duplicate_headers", headers: []string{"60", "120"}},
		{name: "seconds_overflow", headers: []string{"9223372037"}, fatal: true},
		{name: "date_overflow", headers: []string{"Fri, 31 Dec 9999 23:59:59 GMT"}, fatal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, value := range tt.headers {
					w.Header().Add("Retry-After", value)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			w := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)
			err := w.ProcessTask(ctx, &types.DeliveryTask{URL: server.URL})
			require.ErrorContains(t, err, "webhook failed with status: 429")
			assert.Equal(t, tt.fatal, types.IsFatal(err))
			if tt.fatal {
				require.NotNil(t, errors.Unwrap(err))
				assert.ErrorContains(t, errors.Unwrap(err), "webhook failed with status: 429")
				assert.ErrorContains(t, errors.Unwrap(err), "Retry-After exceeds the maximum supported delay")
				assert.NotContains(t, err.Error(), tt.headers[0])
			}
			var hint *types.RetryAfterError
			if tt.want > 0 {
				require.ErrorAs(t, err, &hint)
				assert.Equal(t, tt.want, hint.Delay)
				assert.ErrorContains(t, errors.Unwrap(hint), "webhook failed with status: 429")
			} else {
				assert.False(t, errors.As(err, &hint))
			}
		})
	}
}

func TestParseRetryAfter_DurationLimit(t *testing.T) {
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	lastDate := now.Add(time.Duration(math.MaxInt64)).Truncate(time.Second)
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "largest_whole_seconds", value: "9223372036", want: 9223372036 * time.Second},
		{name: "seconds_overflow", value: "9223372037", wantErr: true},
		{name: "integer_overflow", value: "999999999999999999999999999999", wantErr: true},
		{name: "malformed_large_value", value: "999999999999999999999999999999x"},
		{name: "last_representable_date", value: lastDate.Format(http.TimeFormat), want: lastDate.Sub(now)},
		{name: "date_overflow", value: lastDate.Add(time.Second).Format(http.TimeFormat), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay, err := parseRetryAfter(tt.value, now)
			if tt.wantErr {
				require.ErrorContains(t, err, "Retry-After exceeds the maximum supported delay")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.want, delay)
		})
	}
}

func TestDeliveryWorker_ProcessTask_WithSignature(t *testing.T) {
	// 1. Setup Mock Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Signature Header exists when SecretsRef is provided
		sig := r.Header.Get("X-Syntrix-Signature")
		assert.NotEmpty(t, sig)
		assert.True(t, strings.HasPrefix(sig, "t="))
		assert.Contains(t, sig, ",v1=")

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// 2. Setup Worker with secret provider
	mockSecrets := &MockSecretProvider{
		secrets: map[string]string{"my-secret": "test-secret-value"},
	}

	worker := NewDeliveryWorker(nil, mockSecrets, HTTPClientOptions{}, nil)

	// 3. Create Task with SecretsRef
	task := &types.DeliveryTask{
		TriggerID:  "trig-1",
		URL:        server.URL,
		SecretsRef: "my-secret",
	}

	// 4. Execute
	err := worker.ProcessTask(context.Background(), task)
	assert.NoError(t, err)
}

func TestDeliveryWorker_ProcessTask_SecretsRefWithoutProvider(t *testing.T) {
	// When SecretsRef is provided but no SecretProvider is configured, should fail fatally
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	worker := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)

	task := &types.DeliveryTask{
		TriggerID:  "trig-1",
		URL:        server.URL,
		SecretsRef: "my-secret", // SecretsRef provided but no provider
	}

	err := worker.ProcessTask(context.Background(), task)
	assert.Error(t, err)
	assert.True(t, types.IsFatal(err))
	assert.Contains(t, err.Error(), "no secret provider configured")
}

func TestDeliveryWorker_ProcessTask_WithToken(t *testing.T) {
	mockAuth := new(MockAuthN)
	mockAuth.On("GenerateSystemToken", "trigger-worker").Return("mock-token", nil)

	// 2. Setup Mock Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		task := types.DeliveryTask{}
		err := json.NewDecoder(r.Body).Decode(&task)
		assert.NoError(t, err)

		// Verify Pre-Issued Token
		assert.Equal(t, "mock-token", task.PreIssuedToken)

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	worker := NewDeliveryWorker(mockAuth, nil, HTTPClientOptions{}, nil)
	task := &types.DeliveryTask{
		TriggerID: "trig-1",
		URL:       server.URL,
	}
	err := worker.ProcessTask(context.Background(), task)
	assert.NoError(t, err)
	mockAuth.AssertExpectations(t)
}

func TestDeliveryWorker_ProcessTask_TokenError(t *testing.T) {
	mockAuth := new(MockAuthN)
	mockAuth.On("GenerateSystemToken", "trigger-worker").Return("", fmt.Errorf("token generation failed"))

	worker := NewDeliveryWorker(mockAuth, nil, HTTPClientOptions{}, nil)
	task := &types.DeliveryTask{
		TriggerID: "trig-1",
		URL:       "http://example.com",
	}
	err := worker.ProcessTask(context.Background(), task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to generate system token")
	mockAuth.AssertExpectations(t)
}

func TestDeliveryWorker_ProcessTask_NetworkError(t *testing.T) {
	worker := NewDeliveryWorker(nil, nil, HTTPClientOptions{Timeout: 100 * time.Millisecond}, nil)
	task := &types.DeliveryTask{
		TriggerID: "trig-1",
		URL:       "http://invalid-url-that-does-not-exist.local",
	}
	err := worker.ProcessTask(context.Background(), task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "request failed")
}

func TestDeliveryWorker_ProcessTask_InvalidURL(t *testing.T) {
	worker := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)
	task := &types.DeliveryTask{
		TriggerID: "trig-1",
		URL:       "://invalid-url",
	}
	err := worker.ProcessTask(context.Background(), task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create request")
}

type MockSecretProvider struct {
	secrets   map[string]string
	getSecret func(context.Context, string) (string, error)
}

func (m *MockSecretProvider) GetSecret(ctx context.Context, ref string) (string, error) {
	if m.getSecret != nil {
		return m.getSecret(ctx, ref)
	}
	if s, ok := m.secrets[ref]; ok {
		return s, nil
	}
	return "", fmt.Errorf("secret not found")
}

func TestDeliveryWorker_ProcessTask_WithSecret(t *testing.T) {
	secret := "my-secret-key"

	// 1. Setup Mock Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sig := r.Header.Get("X-Syntrix-Signature")
		// Verify signature is generated using the secret
		// We can't easily verify the HMAC without re-calculating it, but we can check it's present.
		// Or we can calculate it here if we know the timestamp.
		assert.NotEmpty(t, sig)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// 2. Setup Worker with Secret Provider
	secrets := &MockSecretProvider{
		secrets: map[string]string{"secret-ref-1": secret},
	}
	worker := NewDeliveryWorker(nil, secrets, HTTPClientOptions{}, nil)

	// 3. Create Task
	task := &types.DeliveryTask{
		TriggerID:  "trig-1",
		URL:        server.URL,
		SecretsRef: "secret-ref-1",
	}

	// 4. Execute
	err := worker.ProcessTask(context.Background(), task)
	assert.NoError(t, err)
}

func TestDeliveryWorker_ProcessTask_MarshalError(t *testing.T) {
	worker := NewDeliveryWorker(nil, nil, HTTPClientOptions{}, nil)

	// Create Task with unserializable payload
	task := &types.DeliveryTask{
		TriggerID: "trig-1",
		URL:       "http://example.com",
		Payload: map[string]interface{}{
			"bad": make(chan int),
		},
	}

	err := worker.ProcessTask(context.Background(), task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to marshal task")
}

func TestDeliveryWorker_ProcessTask_SecretError(t *testing.T) {
	secrets := &MockSecretProvider{
		secrets: map[string]string{},
	}
	worker := NewDeliveryWorker(nil, secrets, HTTPClientOptions{}, nil)

	task := &types.DeliveryTask{
		TriggerID:  "trig-1",
		URL:        "http://example.com",
		SecretsRef: "missing-secret",
	}

	err := worker.ProcessTask(context.Background(), task)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to resolve secret")
}
