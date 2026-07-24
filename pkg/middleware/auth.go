package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/vsayfb/gig-platform-chat-service/pkg/httputil"
	"github.com/vsayfb/gig-platform-chat-service/pkg/jwt"
)

type contextKey string

const UserIDKey contextKey = "userID"

func Auth(jwtSvc *jwt.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			authHeader := r.Header.Get("Authorization")

			if !strings.HasPrefix(authHeader, "Bearer ") {
				httputil.WriteError(w, http.StatusUnauthorized, "missing or invalid authorization header")
				return
			}

			token := strings.TrimPrefix(authHeader, "Bearer ")

			claims, err := jwtSvc.Verify(token)

			if err != nil {
				httputil.WriteError(w, http.StatusUnauthorized, "invalid token")
				return
			}

			ctx := context.WithValue(r.Context(), UserIDKey, claims.Subject)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GetUserID(ctx context.Context) string {
	id, _ := ctx.Value(UserIDKey).(string)
	return id
}
