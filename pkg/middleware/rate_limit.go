package middleware

import (
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/ratelimit"
	"github.com/vsayfb/gig-platform-chat-service/pkg/httputil"
)

func RateLimitByUser(limiter *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userID := GetUserID(r.Context())
			if userID == "" {
				httputil.WriteError(w, http.StatusUnauthorized, "Unauthorized")
				return
			}

			allowed, retryAfter := limiter.Allow(userID)
			if !allowed {
				w.Header().Set("Retry-After", retryAfterSeconds(retryAfter))
				httputil.WriteError(w, http.StatusTooManyRequests, "Rate limit exceeded")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func retryAfterSeconds(d time.Duration) string {
	seconds := max(int(math.Ceil(d.Seconds())), 1)
	
	return strconv.Itoa(seconds)
}
