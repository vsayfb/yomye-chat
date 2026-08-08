package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/internal/ratelimit"
)

func TestRateLimitByUserReturns429AfterBurst(t *testing.T) {
	limiter := ratelimit.New(1, time.Hour, 1)
	middleware := RateLimitByUser(limiter)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	request := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/private", nil)
		ctx := context.WithValue(r.Context(), UserIDKey, "user-1")
		return r.WithContext(ctx)
	}

	first := httptest.NewRecorder()
	middleware(next).ServeHTTP(first, request())
	if first.Code != http.StatusNoContent {
		t.Fatalf("first status = %d, want 204", first.Code)
	}

	second := httptest.NewRecorder()
	middleware(next).ServeHTTP(second, request())
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header was not set")
	}
}

func TestRateLimitByUserBucketsArePerIdentity(t *testing.T) {
	limiter := ratelimit.New(1, time.Hour, 1)
	middleware := RateLimitByUser(limiter)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	for _, userID := range []string{"alice", "bob"} {
		r := httptest.NewRequest(http.MethodGet, "/private", nil)
		r = r.WithContext(context.WithValue(r.Context(), UserIDKey, userID))
		rr := httptest.NewRecorder()
		middleware(next).ServeHTTP(rr, r)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("%s first status = %d, want 204", userID, rr.Code)
		}
	}
}

func TestRateLimitByUserFailsClosedWithoutAuthenticatedIdentity(t *testing.T) {
	limiter := ratelimit.New(1, time.Hour, 1)
	nextCalled := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { nextCalled = true })

	rr := httptest.NewRecorder()
	RateLimitByUser(limiter)(next).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if nextCalled {
		t.Fatal("next handler was called without an authenticated identity")
	}
}
