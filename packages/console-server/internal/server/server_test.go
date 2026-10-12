package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/auth"
	serverconfig "github.com/codetreker/syntrix/console-server/internal/server/config"
	"github.com/codetreker/syntrix/console-server/internal/workload"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testOrigin = "https://console.example.test"

type accountStub struct {
	login        func(context.Context, string, string) (auth.Session, string, error)
	authenticate func(context.Context, string) (auth.Session, error)
	logout       func(context.Context, string) error
}

func (a accountStub) Login(ctx context.Context, username, password string) (auth.Session, string, error) {
	return a.login(ctx, username, password)
}

func (a accountStub) Authenticate(ctx context.Context, token string) (auth.Session, error) {
	return a.authenticate(ctx, token)
}

func (a accountStub) Logout(ctx context.Context, token string) error {
	return a.logout(ctx, token)
}

type workloadStub struct {
	create func(context.Context, string, string, string) (workload.Workload, error)
	list   func(context.Context, string) ([]workload.Workload, error)
	get    func(context.Context, string, string) (workload.Workload, error)
	delete func(context.Context, string, string) (workload.Workload, error)
}

func (w workloadStub) Create(ctx context.Context, developer, template, key string) (workload.Workload, error) {
	return w.create(ctx, developer, template, key)
}

func (w workloadStub) List(ctx context.Context, developer string) ([]workload.Workload, error) {
	return w.list(ctx, developer)
}

func (w workloadStub) Get(ctx context.Context, developer, id string) (workload.Workload, error) {
	return w.get(ctx, developer, id)
}

func (w workloadStub) Delete(ctx context.Context, developer, id string) (workload.Workload, error) {
	return w.delete(ctx, developer, id)
}

func sessionFixture() auth.Session {
	return auth.Session{Developer: auth.Developer{ID: "dev-alice", Username: "alice"}, CSRFToken: "session-csrf", ExpiresAt: time.Now().UTC().Add(time.Hour).Truncate(time.Second)}
}

func authenticatedAccounts(t *testing.T) accountStub {
	t.Helper()
	return accountStub{authenticate: func(_ context.Context, token string) (auth.Session, error) {
		assert.Equal(t, "opaque-session", token)
		return sessionFixture(), nil
	}}
}

func testHandler(t *testing.T, accounts Accounts, workloads Workloads) http.Handler {
	t.Helper()
	cfg := serverconfig.DefaultConfig()
	cfg.PublicOrigin = testOrigin
	s, err := New(cfg, accounts, workloads, []string{"http-test"}, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return nil })
	require.NoError(t, err)
	return s.Handler()
}

func request(method, path, body string, authenticated bool) *http.Request {
	r := httptest.NewRequest(method, testOrigin+path, strings.NewReader(body))
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/json")
	if authenticated {
		r.AddCookie(&http.Cookie{Name: "console_session", Value: "opaque-session"})
		r.Header.Set("X-CSRF-Token", "session-csrf")
	}
	return r
}

func TestLoginIssuesProtectedOpaqueCookie(t *testing.T) {
	session := sessionFixture()
	handler := testHandler(t, accountStub{login: func(_ context.Context, username, password string) (auth.Session, string, error) {
		assert.Equal(t, "alice", username)
		assert.Equal(t, "private-password", password)
		return session, "opaque-session", nil
	}}, workloadStub{})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request(http.MethodPost, "/auth/v1/login", `{"username":"alice","password":"private-password"}`, false))
	require.Equal(t, http.StatusOK, w.Code)
	var actual auth.Session
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &actual))
	assert.Equal(t, session, actual)
	assert.NotContains(t, w.Body.String(), "opaque-session")
	assert.NotContains(t, w.Body.String(), "private-password")
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	cookie := cookies[0]
	assert.Equal(t, "console_session", cookie.Name)
	assert.Equal(t, "opaque-session", cookie.Value)
	assert.Equal(t, "/", cookie.Path)
	assert.True(t, cookie.HttpOnly)
	assert.True(t, cookie.Secure)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.Equal(t, session.ExpiresAt, cookie.Expires)
	assert.Positive(t, cookie.MaxAge)
	assert.LessOrEqual(t, cookie.MaxAge, 3600)
	assert.Regexp(t, `^[0-9a-f]{32}$`, w.Header().Get("X-Request-ID"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"))
}

func TestLoginRejectsInvalidCredentialsWithoutCookie(t *testing.T) {
	handler := testHandler(t, accountStub{login: func(context.Context, string, string) (auth.Session, string, error) {
		return auth.Session{}, "", fmt.Errorf("credential check: %w", auth.ErrUnauthorized)
	}}, workloadStub{})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request(http.MethodPost, "/auth/v1/login", `{"username":"alice","password":"wrong"}`, false))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Result().Cookies())
	assert.JSONEq(t, `{"error":"authentication required"}`, w.Body.String())
}

func TestMeUsesPersistedSessionIdentity(t *testing.T) {
	handler := testHandler(t, authenticatedAccounts(t), workloadStub{})
	w := httptest.NewRecorder()
	r := request(http.MethodGet, "/auth/v1/me?developer_id=dev-victim", "", true)
	r.Header.Set("X-Developer-ID", "dev-victim")
	handler.ServeHTTP(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	var session auth.Session
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &session))
	assert.Equal(t, "dev-alice", session.Developer.ID)
	assert.Equal(t, "session-csrf", session.CSRFToken)
}

func TestProtectedRoutesNeverCallDependenciesWithoutSession(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/auth/v1/me"},
		{http.MethodPost, "/auth/v1/logout"},
		{http.MethodGet, "/api/v1/templates"},
		{http.MethodPost, "/api/v1/workloads"},
		{http.MethodGet, "/api/v1/workloads"},
		{http.MethodGet, "/api/v1/workloads/foreign"},
		{http.MethodDelete, "/api/v1/workloads/foreign"},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			testHandler(t, accountStub{}, workloadStub{}).ServeHTTP(w, request(route.method, route.path, `{"template_id":"http-test"}`, false))
			assert.Equal(t, http.StatusUnauthorized, w.Code)
		})
	}
}

func TestMutationRequiresTrustedOriginAndSessionCSRF(t *testing.T) {
	routes := []struct {
		method, path string
		login        bool
	}{
		{http.MethodPost, "/auth/v1/login", true},
		{http.MethodPost, "/auth/v1/logout", false},
		{http.MethodPost, "/api/v1/workloads", false},
		{http.MethodDelete, "/api/v1/workloads/workload-1", false},
	}
	for _, route := range routes {
		t.Run(route.path, func(t *testing.T) {
			for _, origin := range []string{"", "https://attacker.example", "https://console.example.test/"} {
				r := request(route.method, route.path, `{}`, !route.login)
				r.Header.Set("Origin", origin)
				w := httptest.NewRecorder()
				testHandler(t, accountStub{}, workloadStub{}).ServeHTTP(w, r)
				assert.Equal(t, http.StatusForbidden, w.Code)
			}
			if route.login {
				return
			}
			for _, token := range []string{"", "wrong-token"} {
				r := request(route.method, route.path, `{}`, true)
				r.Header.Set("X-CSRF-Token", token)
				w := httptest.NewRecorder()
				testHandler(t, authenticatedAccounts(t), workloadStub{}).ServeHTTP(w, r)
				assert.Equal(t, http.StatusForbidden, w.Code)
			}
		})
	}
}

func TestLogoutRevokesSessionAndExpiresCookie(t *testing.T) {
	accounts := authenticatedAccounts(t)
	accounts.logout = func(_ context.Context, token string) error {
		assert.Equal(t, "opaque-session", token)
		return nil
	}
	w := httptest.NewRecorder()
	testHandler(t, accounts, workloadStub{}).ServeHTTP(w, request(http.MethodPost, "/auth/v1/logout", "", true))
	require.Equal(t, http.StatusNoContent, w.Code)
	cookies := w.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Empty(t, cookies[0].Value)
	assert.Equal(t, -1, cookies[0].MaxAge)
	assert.True(t, cookies[0].HttpOnly)
	assert.True(t, cookies[0].Secure)
	assert.True(t, cookies[0].Expires.Before(time.Now()))
}

func TestRejectedSessionNeverCallsWorkloads(t *testing.T) {
	accounts := accountStub{authenticate: func(context.Context, string) (auth.Session, error) {
		return auth.Session{}, fmt.Errorf("expired session: %w", auth.ErrUnauthorized)
	}}
	w := httptest.NewRecorder()
	testHandler(t, accounts, workloadStub{}).ServeHTTP(w, request(http.MethodPost, "/api/v1/workloads", `{"template_id":"http-test"}`, true))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestLogoutFailureKeepsCookieUntilRevocationSucceeds(t *testing.T) {
	accounts := authenticatedAccounts(t)
	accounts.logout = func(context.Context, string) error { return errors.New("revocation unavailable") }
	w := httptest.NewRecorder()
	testHandler(t, accounts, workloadStub{}).ServeHTTP(w, request(http.MethodPost, "/auth/v1/logout", "", true))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Empty(t, w.Result().Cookies())
}

func TestWorkloadRoutesForwardAuthenticatedOwner(t *testing.T) {
	item := workload.Workload{ID: "workload-1", DeveloperID: "dev-alice", TemplateID: "http-test", Status: workload.StatusPending, Snapshot: workload.Template{Env: map[string]string{"SECRET": "template-secret"}}}
	workloads := workloadStub{
		create: func(_ context.Context, developer, template, key string) (workload.Workload, error) {
			assert.Equal(t, "dev-alice", developer)
			assert.Equal(t, "http-test", template)
			assert.Equal(t, "stable-create-key", key)
			return item, nil
		},
		list: func(_ context.Context, developer string) ([]workload.Workload, error) {
			assert.Equal(t, "dev-alice", developer)
			return []workload.Workload{item}, nil
		},
		get: func(_ context.Context, developer, id string) (workload.Workload, error) {
			assert.Equal(t, "dev-alice", developer)
			assert.Equal(t, "workload-1", id)
			return item, nil
		},
		delete: func(_ context.Context, developer, id string) (workload.Workload, error) {
			assert.Equal(t, "dev-alice", developer)
			assert.Equal(t, "workload-1", id)
			return item, nil
		},
	}
	routes := []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/api/v1/workloads", `{"template_id":"http-test"}`, http.StatusAccepted},
		{http.MethodGet, "/api/v1/workloads", "", http.StatusOK},
		{http.MethodGet, "/api/v1/workloads/workload-1", "", http.StatusOK},
		{http.MethodDelete, "/api/v1/workloads/workload-1", "", http.StatusAccepted},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			r := request(route.method, route.path+"?developer_id=dev-victim", route.body, true)
			r.Header.Set("X-Developer-ID", "dev-victim")
			r.Header.Set("Idempotency-Key", "stable-create-key")
			w := httptest.NewRecorder()
			testHandler(t, authenticatedAccounts(t), workloads).ServeHTTP(w, r)
			assert.Equal(t, route.status, w.Code)
			assert.NotContains(t, w.Body.String(), "template-secret")
			if route.method == http.MethodPost {
				assert.Equal(t, "/api/v1/workloads/workload-1", w.Header().Get("Location"))
			}
		})
	}
}

func TestDeletedWorkloadReturnsCompletedStatus(t *testing.T) {
	workloads := workloadStub{delete: func(context.Context, string, string) (workload.Workload, error) {
		return workload.Workload{ID: "workload-1", Status: workload.StatusDeleted}, nil
	}}
	w := httptest.NewRecorder()
	testHandler(t, authenticatedAccounts(t), workloads).ServeHTTP(w, request(http.MethodDelete, "/api/v1/workloads/workload-1", "", true))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestTemplateListingUsesCopiedAllowlist(t *testing.T) {
	cfg := serverconfig.DefaultConfig()
	cfg.PublicOrigin = testOrigin
	templates := []string{"http-test"}
	s, err := New(cfg, authenticatedAccounts(t), workloadStub{}, templates, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return nil })
	require.NoError(t, err)
	templates[0] = "untrusted-template"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request(http.MethodGet, "/api/v1/templates", "", true))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `[{"id":"http-test"}]`, w.Body.String())
}

func TestJSONRequestRejectsUnboundedOrAmbiguousInput(t *testing.T) {
	tests := []struct {
		name, body, contentType string
		status                  int
	}{
		{"unknown field", `{"username":"alice","password":"secret","developer_id":"victim"}`, "application/json", http.StatusBadRequest},
		{"trailing value", `{"username":"alice","password":"secret"} {}`, "application/json", http.StatusBadRequest},
		{"trailing malformed input", `{"username":"alice"} !`, "application/json", http.StatusBadRequest},
		{"malformed body", `{"username":`, "application/json", http.StatusBadRequest},
		{"oversized body", `{"username":"` + strings.Repeat("x", 4096) + `"}`, "application/json", http.StatusBadRequest},
		{"missing content type", `{}`, "", http.StatusUnsupportedMediaType},
		{"form body", `username=alice&password=secret`, "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := request(http.MethodPost, "/auth/v1/login", test.body, false)
			r.Header.Set("Content-Type", test.contentType)
			w := httptest.NewRecorder()
			testHandler(t, accountStub{}, workloadStub{}).ServeHTTP(w, r)
			assert.Equal(t, test.status, w.Code)
			assert.NotContains(t, w.Body.String(), "secret")
		})
	}
	t.Run("caller cannot inject workload owner", func(t *testing.T) {
		w := httptest.NewRecorder()
		testHandler(t, authenticatedAccounts(t), workloadStub{}).ServeHTTP(w, request(http.MethodPost, "/api/v1/workloads", `{"template_id":"http-test","developer_id":"victim"}`, true))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestDependencyErrorsMapToSafeResponses(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code int
	}{
		{"foreign or missing resource", fmt.Errorf("owner query: %w", workload.ErrNotFound), http.StatusNotFound},
		{"conflicting request", fmt.Errorf("idempotency: %w", workload.ErrConflict), http.StatusConflict},
		{"invalid request", fmt.Errorf("unknown template: %w", workload.ErrInvalid), http.StatusBadRequest},
		{"dependency unavailable", errors.New("postgres://private-user:private-password@db/private-db"), http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			cfg := serverconfig.DefaultConfig()
			cfg.PublicOrigin = testOrigin
			s, err := New(cfg, authenticatedAccounts(t), workloadStub{get: func(context.Context, string, string) (workload.Workload, error) { return workload.Workload{}, test.err }}, nil, slog.New(slog.NewJSONHandler(&logs, nil)), func(context.Context) error { return nil })
			require.NoError(t, err)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, request(http.MethodGet, "/api/v1/workloads/foreign", "", true))
			assert.Equal(t, test.code, w.Code)
			assert.NotContains(t, w.Body.String()+logs.String(), "private-password")
			assert.NotContains(t, w.Body.String()+logs.String(), "postgres://")
			assert.NotEmpty(t, w.Header().Get("X-Request-ID"))
			if test.code == http.StatusServiceUnavailable {
				assert.Contains(t, logs.String(), w.Header().Get("X-Request-ID"))
			}
		})
	}
}

func TestWorkloadMutationAndListErrorsPreserveStatus(t *testing.T) {
	for _, route := range []struct {
		method, path string
		code         int
	}{
		{http.MethodPost, "/api/v1/workloads", http.StatusConflict},
		{http.MethodGet, "/api/v1/workloads", http.StatusServiceUnavailable},
		{http.MethodDelete, "/api/v1/workloads/foreign", http.StatusNotFound},
	} {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			workloads := workloadStub{
				create: func(context.Context, string, string, string) (workload.Workload, error) {
					return workload.Workload{}, workload.ErrConflict
				},
				list: func(context.Context, string) ([]workload.Workload, error) {
					return nil, errors.New("source database unavailable")
				},
				delete: func(context.Context, string, string) (workload.Workload, error) {
					return workload.Workload{}, workload.ErrNotFound
				},
			}
			w := httptest.NewRecorder()
			testHandler(t, authenticatedAccounts(t), workloads).ServeHTTP(w, request(route.method, route.path, `{"template_id":"http-test"}`, true))
			assert.Equal(t, route.code, w.Code)
			assert.Empty(t, w.Header().Get("Location"))
		})
	}
}

func TestReadinessDoesNotExposeDependencyDetails(t *testing.T) {
	cfg := serverconfig.DefaultConfig()
	cfg.PublicOrigin = testOrigin
	s, err := New(cfg, accountStub{}, workloadStub{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		assert.True(t, ok)
		assert.LessOrEqual(t, time.Until(deadline), 3*time.Second)
		return errors.New("database password=sensitive")
	})
	require.NoError(t, err)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request(http.MethodGet, "/health/ready", "", false))
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.NotContains(t, w.Body.String(), "sensitive")
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, request(http.MethodGet, "/health/live", "", false))
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSuccessfulReadiness(t *testing.T) {
	w := httptest.NewRecorder()
	testHandler(t, accountStub{}, workloadStub{}).ServeHTTP(w, request(http.MethodGet, "/health/ready", "", false))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"status":"ok"}`, w.Body.String())
}

func TestRequestTimeoutBoundsAuthenticationAndWorkloadDependencies(t *testing.T) {
	for _, operation := range []string{"login", "authentication", "workload"} {
		t.Run(operation, func(t *testing.T) {
			cfg := serverconfig.DefaultConfig()
			cfg.PublicOrigin = testOrigin
			cfg.RequestTimeout = 30 * time.Millisecond
			cfg.WriteTimeout = 100 * time.Millisecond
			awaitCancellation := func(ctx context.Context) error {
				deadline, ok := ctx.Deadline()
				assert.True(t, ok)
				assert.LessOrEqual(t, time.Until(deadline), cfg.RequestTimeout)
				<-ctx.Done()
				return ctx.Err()
			}
			accounts := authenticatedAccounts(t)
			accounts.login = func(ctx context.Context, _, _ string) (auth.Session, string, error) {
				return auth.Session{}, "", awaitCancellation(ctx)
			}
			method, path, body := http.MethodPost, "/auth/v1/login", `{"username":"alice","password":"password"}`
			if operation == "authentication" {
				accounts.authenticate = func(ctx context.Context, _ string) (auth.Session, error) {
					return auth.Session{}, awaitCancellation(ctx)
				}
				method, path, body = http.MethodGet, "/auth/v1/me", ""
			}
			if operation == "workload" {
				method, path, body = http.MethodPost, "/api/v1/workloads", `{"template_id":"http-test"}`
			}
			workloads := workloadStub{create: func(ctx context.Context, _, _, _ string) (workload.Workload, error) {
				return workload.Workload{}, awaitCancellation(ctx)
			}}
			s, err := New(cfg, accounts, workloads, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) error { return nil })
			require.NoError(t, err)
			w := httptest.NewRecorder()
			start := time.Now()
			s.Handler().ServeHTTP(w, request(method, path, body, operation != "login"))
			assert.Equal(t, http.StatusServiceUnavailable, w.Code)
			assert.Less(t, time.Since(start), time.Second)
			assert.Empty(t, w.Result().Cookies())
		})
	}
}

func TestNewRejectsMissingDependencies(t *testing.T) {
	cfg := serverconfig.DefaultConfig()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ready := func(context.Context) error { return nil }
	for _, test := range []struct {
		name      string
		accounts  Accounts
		workloads Workloads
		logger    *slog.Logger
		ready     func(context.Context) error
	}{
		{"accounts", nil, workloadStub{}, logger, ready},
		{"workloads", accountStub{}, nil, logger, ready},
		{"logger", accountStub{}, workloadStub{}, nil, ready},
		{"readiness", accountStub{}, workloadStub{}, logger, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(cfg, test.accounts, test.workloads, nil, test.logger, test.ready)
			assert.ErrorContains(t, err, "dependencies are required")
		})
	}
	cfg.PublicOrigin = "http://public.example"
	_, err := New(cfg, accountStub{}, workloadStub{}, nil, logger, ready)
	assert.ErrorContains(t, err, "HTTPS")
}
