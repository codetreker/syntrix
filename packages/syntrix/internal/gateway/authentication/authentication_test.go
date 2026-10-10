package authentication

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/ctxkeys"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMiddlewareParserAndContext(t *testing.T) {
	for _, optional := range []bool{false, true} {
		for _, tc := range []struct {
			header, body string
			status       int
			validate     bool
		}{
			{"", "Authorization header required\n", 401, false},
			{"Bearer good", "", 204, true},
			{"Bearer invalid", "Invalid or expired token\n", 401, true},
			{"Bearer ", "Invalid or expired token\n", 401, true},
			{"Bearer", "Invalid authorization header format\n", 401, false},
			{"bearer good", "Invalid authorization header format\n", 401, false},
			{"Bearer  good", "Invalid authorization header format\n", 401, false},
			{" Bearer good", "Invalid authorization header format\n", 401, false},
			{"Bearer good ", "Invalid authorization header format\n", 401, false},
			{"Bearer\tgood", "Invalid authorization header format\n", 401, false},
		} {
			t.Run(tc.header, func(t *testing.T) {
				calls := 0
				claims := &identity.Claims{Username: "operator", Roles: []string{"admin"}, DBAdmin: []string{"db"}, UserID: "oid",
					RegisteredClaims: jwt.RegisteredClaims{Subject: "subject", Audience: jwt.ClaimStrings{"aud"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}
				verifier, err := identity.NewVerifier(func(token string) (*identity.Claims, error) {
					calls++
					if token != "good" {
						return nil, identity.ErrInvalidToken
					}
					return claims, nil
				})
				require.NoError(t, err)
				next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if tc.header == "" {
						assert.Nil(t, FromContext(r.Context()))
						assert.Nil(t, r.Context().Value(ctxkeys.KeyUserID))
					} else {
						actor := FromContext(r.Context())
						require.NotNil(t, actor)
						assert.Equal(t, "subject", r.Context().Value(ctxkeys.KeyUserID))
						assert.Equal(t, "operator", r.Context().Value(ctxkeys.KeyUsername))
						assert.Equal(t, claims.Roles, r.Context().Value(ctxkeys.KeyRoles))
						assert.Equal(t, claims.DBAdmin, r.Context().Value(ctxkeys.KeyDBAdmin))
						projection := r.Context().Value(ctxkeys.KeyClaims).(*identity.Claims)
						assert.Equal(t, claims, projection)
						projection.Roles[0] = "user"
						require.NoError(t, verifier.AuthorizeAdmin(actor))
					}
					w.WriteHeader(204)
				})
				auth := New(verifier)
				handler := auth.Middleware(next)
				if optional {
					handler = auth.MiddlewareOptional(next)
				}
				request := httptest.NewRequest(http.MethodGet, "/", nil)
				request.Header.Set("Authorization", tc.header)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				status, body := tc.status, tc.body
				if optional && tc.header == "" {
					status, body = 204, ""
				}
				assert.Equal(t, status, response.Code)
				assert.Equal(t, body, response.Body.String())
				if tc.validate {
					assert.Equal(t, 1, calls)
				} else {
					assert.Zero(t, calls)
				}
			})
		}
	}
}

type nilActorVerifier struct{}

func (nilActorVerifier) VerifyToken(string) (*identity.VerifiedIdentity, error) { return nil, nil }

func TestMiddlewareRejectsMissingActor(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	New(nilActorVerifier{}).Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler called") })).ServeHTTP(response, request)
	assert.Equal(t, 401, response.Code)
	assert.Equal(t, "Invalid or expired token\n", response.Body.String())
}
