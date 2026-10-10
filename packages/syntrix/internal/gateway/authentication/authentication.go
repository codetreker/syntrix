package authentication

import (
	"context"
	"net/http"
	"strings"

	"github.com/codetreker/syntrix/internal/ctxkeys"
	"github.com/codetreker/syntrix/internal/identity"
)

type actorContextKey struct{}

type Authenticator struct{ verifier identity.TokenVerifier }

func New(verifier identity.TokenVerifier) *Authenticator {
	return &Authenticator{verifier: verifier}
}

func FromContext(ctx context.Context) *identity.VerifiedIdentity {
	actor, _ := ctx.Value(actorContextKey{}).(*identity.VerifiedIdentity)
	return actor
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return a.middleware(next, false)
}

func (a *Authenticator) MiddlewareOptional(next http.Handler) http.Handler {
	return a.middleware(next, true)
}

func (a *Authenticator) middleware(next http.Handler, optional bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			if optional {
				next.ServeHTTP(w, r)
				return
			}
			http.Error(w, "Authorization header required", http.StatusUnauthorized)
			return
		}
		parts := strings.Split(header, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			http.Error(w, "Invalid authorization header format", http.StatusUnauthorized)
			return
		}
		actor, err := a.verifier.VerifyToken(parts[1])
		if err != nil || actor == nil {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}
		claims := actor.Claims()
		ctx := context.WithValue(r.Context(), actorContextKey{}, actor)
		ctx = context.WithValue(ctx, ctxkeys.KeyUserID, claims.Subject)
		ctx = context.WithValue(ctx, ctxkeys.KeyUsername, claims.Username)
		ctx = context.WithValue(ctx, ctxkeys.KeyRoles, claims.Roles)
		ctx = context.WithValue(ctx, ctxkeys.KeyClaims, claims)
		ctx = context.WithValue(ctx, ctxkeys.KeyDBAdmin, claims.DBAdmin)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
